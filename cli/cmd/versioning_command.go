package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"curvature/backend/app"
)

// versioningCommandRequest is a parsed `-versioning` invocation.
type versioningCommandRequest struct {
	Action   string
	RootID   string
	Revision string
	Message  string
	Mode     string
	Limit    int
	// Name is the timeline name for `branch` and `checkout`.
	Name string
	// Keep is how many automatic checkpoints `prune` retains. A negative value
	// leaves the choice to the server default.
	Keep int
}

// versioningCLIActions lists the accepted action names, for error messages.
const versioningCLIActions = "info, status, log, snapshot, restore, branches, branch, checkout, gc, prune, verify"

// agentSessionEnvKey marks a shell an agent issued, set by commandexec. Kept as a
// literal because the CLI reaches the server over HTTP and does not link the
// command package; the two must stay in sync.
const agentSessionEnvKey = "CURVATURE_AGENT_SESSION"

// agentSessionHeader tells the server the caller is an agent rather than the
// user's own terminal. The CLI always declares itself when the marker is
// present and lets the server decide which actions that restricts, so the policy
// lives in exactly one place.
const agentSessionHeader = "X-Curvature-Agent-Session"

// agentSessionFromEnv reports the session commandexec recorded for this shell,
// or "" when the CLI was run from the user's own terminal. A blank value counts
// as human: declaring an empty session would be meaningless.
func agentSessionFromEnv() string {
	return strings.TrimSpace(os.Getenv(agentSessionEnvKey))
}

// handleVersioningCommand drives the local version store through the running
// server, so the CLI sees exactly what the web UI sees.
func handleVersioningCommand(addr string, useTLS bool, req versioningCommandRequest) error {
	rootID := strings.TrimSpace(req.RootID)
	if rootID == "" {
		return errors.New("root id argument required: curvature <rootid> -versioning <action>")
	}
	method, path, body, err := versioningRequest(rootID, req)
	if err != nil {
		return err
	}
	token, err := app.ReadLocalCLIToken(addr)
	if err != nil {
		return err
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequest(method, addrToURL(addr, path, useTLS), reader)
	if err != nil {
		return err
	}
	httpReq.Header.Set("X-Curvature-Local-CLI-Token", token)
	if session := agentSessionFromEnv(); session != "" {
		httpReq.Header.Set(agentSessionHeader, session)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	client := newHTTPClient(useTLS, 30*time.Second)
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Restores and garbage collection can report a long result, so allow more
	// than the default page size but still bound the read.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(payload))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("versioning %s failed: %s", req.Action, message)
	}
	if req.Action == "status" {
		return printVersioningStatus(payload)
	}
	if req.Action == "branches" {
		return printVersioningBranches(payload)
	}
	if len(payload) == 0 {
		return nil
	}
	if _, err := os.Stdout.Write(payload); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout)
	return err
}

// versioningRequest maps an action onto the HTTP API.
//
// `root` travels as a query parameter for reads and inside the JSON body for
// writes, matching the rest of the versioning API.
func versioningRequest(rootID string, req versioningCommandRequest) (string, string, map[string]any, error) {
	query := url.Values{}
	query.Set("root", rootID)

	switch req.Action {
	case "info":
		return http.MethodGet, "/api/versioning/info?" + query.Encode(), nil, nil
	case "status":
		return http.MethodGet, "/api/versioning/status?" + query.Encode(), nil, nil
	case "log":
		limit := req.Limit
		if limit <= 0 {
			limit = 20
		}
		query.Set("limit", strconv.Itoa(limit))
		return http.MethodGet, "/api/versioning/log?" + query.Encode(), nil, nil
	case "verify":
		return http.MethodGet, "/api/versioning/verify?" + query.Encode(), nil, nil
	case "snapshot":
		body := map[string]any{"root": rootID}
		if message := strings.TrimSpace(req.Message); message != "" {
			body["message"] = message
		}
		return http.MethodPost, "/api/versioning/snapshots", body, nil
	case "restore":
		revision := strings.TrimSpace(req.Revision)
		if revision == "" {
			return "", "", nil, errors.New("-rev is required for -versioning restore")
		}
		body := map[string]any{"root": rootID, "revision": revision}
		if mode := strings.TrimSpace(req.Mode); mode != "" {
			body["mode"] = mode
		}
		return http.MethodPost, "/api/versioning/restore", body, nil
	case "branches":
		return http.MethodGet, "/api/versioning/branches?" + query.Encode(), nil, nil
	case "branch":
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return "", "", nil, errors.New("-name is required for -versioning branch")
		}
		body := map[string]any{"root": rootID, "name": name}
		// Omitting one lets the server branch from the current head.
		if from := strings.TrimSpace(req.Revision); from != "" {
			body["from"] = from
		}
		return http.MethodPost, "/api/versioning/branches", body, nil
	case "checkout":
		name := strings.TrimSpace(req.Name)
		if name == "" {
			return "", "", nil, errors.New("-name is required for -versioning checkout")
		}
		body := map[string]any{"root": rootID, "name": name}
		if mode := strings.TrimSpace(req.Mode); mode != "" {
			body["mode"] = mode
		}
		return http.MethodPost, "/api/versioning/checkout", body, nil
	case "gc":
		return http.MethodPost, "/api/versioning/gc", map[string]any{"root": rootID}, nil
	case "prune":
		body := map[string]any{"root": rootID}
		if req.Keep >= 0 {
			body["keep"] = req.Keep
		}
		return http.MethodPost, "/api/versioning/checkpoints/prune", body, nil
	case "":
		return "", "", nil, fmt.Errorf("-versioning requires an action: %s", versioningCLIActions)
	default:
		return "", "", nil, fmt.Errorf("unknown versioning action %q: expected one of %s", req.Action, versioningCLIActions)
	}
}

// versioningStatusResponse mirrors the subset of the status payload the CLI
// renders. Unknown fields are ignored so a newer server still works.
type versioningStatusResponse struct {
	Status struct {
		Initialized bool   `json:"initialized"`
		Branch      string `json:"branch"`
		Head        string `json:"head"`
		Clean       bool   `json:"clean"`
		Dirty       int    `json:"dirty"`
		Truncated   bool   `json:"truncated"`
		Items       []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"items"`
	} `json:"status"`
}

// printVersioningStatus renders a short summary for the most common action. Any
// payload it cannot parse is echoed verbatim rather than swallowed.
func printVersioningStatus(payload []byte) error {
	var parsed versioningStatusResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		if _, writeErr := os.Stdout.Write(payload); writeErr != nil {
			return writeErr
		}
		_, writeErr := fmt.Fprintln(os.Stdout)
		return writeErr
	}
	status := parsed.Status
	if !status.Initialized {
		_, err := fmt.Fprintln(os.Stdout, "versioning: not initialized for this root")
		return err
	}
	head := status.Head
	if len(head) > 8 {
		head = head[:8]
	}
	if _, err := fmt.Fprintf(os.Stdout, "branch %s  head %s\n", status.Branch, head); err != nil {
		return err
	}
	if status.Clean {
		_, err := fmt.Fprintln(os.Stdout, "working tree is clean")
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "%d changed file(s):\n", status.Dirty); err != nil {
		return err
	}
	for _, item := range status.Items {
		if _, err := fmt.Fprintf(os.Stdout, "  %-8s %s\n", item.Status, item.Path); err != nil {
			return err
		}
	}
	if status.Truncated {
		_, err := fmt.Fprintln(os.Stdout, "  (list truncated)")
		return err
	}
	return nil
}

// versioningBranchesResponse mirrors the branches payload the CLI renders.
type versioningBranchesResponse struct {
	Branches []struct {
		Name    string `json:"name"`
		Head    string `json:"head"`
		Current bool   `json:"current"`
		Subject string `json:"subject"`
	} `json:"branches"`
}

// truncateRunes shortens text by runes so multi-byte content is never split.
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// printVersioningBranches lists the timelines, marking the current one. Any
// payload it cannot parse is echoed verbatim rather than swallowed.
func printVersioningBranches(payload []byte) error {
	var parsed versioningBranchesResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		if _, writeErr := os.Stdout.Write(payload); writeErr != nil {
			return writeErr
		}
		_, writeErr := fmt.Fprintln(os.Stdout)
		return writeErr
	}
	if len(parsed.Branches) == 0 {
		_, err := fmt.Fprintln(os.Stdout, "no timelines")
		return err
	}
	for _, branch := range parsed.Branches {
		marker := " "
		if branch.Current {
			marker = "*"
		}
		head := branch.Head
		if len(head) > 8 {
			head = head[:8]
		}
		if _, err := fmt.Fprintf(
			os.Stdout,
			"%s %-24s %s  %s\n",
			marker,
			truncateRunes(branch.Name, 24),
			head,
			truncateRunes(branch.Subject, 48),
		); err != nil {
			return err
		}
	}
	return nil
}

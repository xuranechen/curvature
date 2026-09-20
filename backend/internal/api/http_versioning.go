package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"curvature/backend/internal/api/usecase"
	"curvature/backend/internal/versioning"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// versioningErrorStatus maps a store error onto an HTTP status.
//
// A missing store is a conflict rather than a server error: the caller can fix
// it by taking a first snapshot.
func versioningErrorStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, versioning.ErrNotInitialized):
		return http.StatusConflict
	case errors.Is(err, versioning.ErrPathNotFound), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func respondVersioningError(w http.ResponseWriter, err error) {
	respondError(w, versioningErrorStatus(err), err)
}

// versioningRoot reads the required `root` query parameter.
func versioningRoot(w http.ResponseWriter, r *http.Request) (string, bool) {
	rootID := strings.TrimSpace(r.URL.Query().Get("root"))
	if rootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return "", false
	}
	return rootID, true
}

// versioningIntQuery parses an optional integer query parameter. A malformed
// value is reported instead of being silently ignored.
func versioningIntQuery(r *http.Request, name string) (int, bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, errInvalidRequest("invalid " + name)
	}
	return value, true, nil
}

func decodeVersioningBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		respondError(w, http.StatusBadRequest, errInvalidRequest("invalid json body"))
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// info / status
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningInfo(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	out, err := h.service().GetVersioningInfo(r.Context(), usecase.VersioningInfoInput{RootID: rootID})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningStatus(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	limit, _, err := versioningIntQuery(r, "limit")
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	out, err := h.service().GetVersioningStatus(r.Context(), usecase.VersioningStatusInput{
		RootID: rootID,
		Limit:  limit,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// snapshots
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID     string `json:"root"`
		Message    string `json:"message"`
		Author     string `json:"author"`
		Tag        string `json:"tag"`
		OriginKind string `json:"origin_kind"`
		SessionID  string `json:"session_id"`
		TaskID     string `json:"task_id"`
		Force      bool   `json:"force"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	out, err := h.service().CreateVersioningSnapshot(r.Context(), usecase.VersioningSnapshotInput{
		RootID:     req.RootID,
		Message:    req.Message,
		Author:     req.Author,
		Tag:        req.Tag,
		OriginKind: req.OriginKind,
		SessionID:  req.SessionID,
		TaskID:     req.TaskID,
		Force:      req.Force,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningLog(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	limit, _, err := versioningIntQuery(r, "limit")
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	query := r.URL.Query()
	out, err := h.service().GetVersioningLog(r.Context(), usecase.VersioningLogInput{
		RootID: rootID,
		Limit:  limit,
		Before: strings.TrimSpace(query.Get("before")),
		Branch: strings.TrimSpace(query.Get("branch")),
		Path:   strings.TrimSpace(query.Get("path")),
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningChanges(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	out, err := h.service().GetVersioningChanges(r.Context(), usecase.VersioningChangesInput{
		RootID: rootID,
		From:   strings.TrimSpace(query.Get("from")),
		To:     strings.TrimSpace(query.Get("to")),
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningTimeline(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("path required"))
		return
	}
	limit, _, err := versioningIntQuery(r, "limit")
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	out, err := h.service().GetVersioningTimeline(r.Context(), usecase.VersioningTimelineInput{
		RootID: rootID,
		Path:   path,
		Limit:  limit,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// content access
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningFileDiff(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("path required"))
		return
	}
	context, _, err := versioningIntQuery(r, "context")
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	query := r.URL.Query()
	out, err := h.service().GetVersioningFileDiff(r.Context(), usecase.VersioningFileDiffInput{
		RootID:  rootID,
		From:    strings.TrimSpace(query.Get("from")),
		To:      strings.TrimSpace(query.Get("to")),
		Path:    path,
		Context: context,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningFile(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("path required"))
		return
	}
	maxBytes, _, err := versioningIntQuery(r, "max_bytes")
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	out, err := h.service().GetVersioningFile(r.Context(), usecase.VersioningFileInput{
		RootID:   rootID,
		Path:     path,
		Revision: strings.TrimSpace(r.URL.Query().Get("revision")),
		MaxBytes: maxBytes,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningTree(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	out, err := h.service().GetVersioningTree(r.Context(), usecase.VersioningTreeInput{
		RootID:   rootID,
		Revision: strings.TrimSpace(r.URL.Query().Get("revision")),
		Dir:      strings.TrimSpace(r.URL.Query().Get("dir")),
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// restore
// ---------------------------------------------------------------------------

// agentSessionHeader marks a request as coming from an agent's shell rather than
// from the user's own terminal.
//
// The local CLI token is shared by both callers, so the server cannot tell them
// apart on its own: the CLI sets this header when commandexec put
// CURVATURE_AGENT_SESSION into the command environment. The check is therefore a
// guardrail against an agent rewriting the working tree by accident, not a
// security boundary — an agent that unsets that variable, or reads the CLI token
// and calls the API directly, is indistinguishable from the user.
const agentSessionHeader = "X-Curvature-Agent-Session"

// allowAgentWorkingTreeRewrite refuses a working-tree rewrite requested by an
// agent unless the user has granted that authority (D11). A human caller is
// never blocked, because the header is absent for them.
func (h *HTTPHandler) allowAgentWorkingTreeRewrite(w http.ResponseWriter, r *http.Request, action string) bool {
	if strings.TrimSpace(r.Header.Get(agentSessionHeader)) == "" {
		return true
	}
	if h.AppContext == nil {
		respondError(w, http.StatusServiceUnavailable, errInvalidRequest("preferences not configured"))
		return false
	}
	if prefs := h.AppContext.GetPreferences(); prefs != nil && prefs.VersioningAgentAutonomyEnabled() {
		return true
	}
	respondError(w, http.StatusForbidden, errInvalidRequest(
		"an agent may not "+action+
			": turn on \"allow agents to restore the working tree\" in settings first"))
	return false
}

func (h *HTTPHandler) handleVersioningRestore(w http.ResponseWriter, r *http.Request) {
	// Rewriting the working tree is the one versioning operation an agent may not
	// take on its own initiative.
	if !h.allowAgentWorkingTreeRewrite(w, r, "restore the working tree") {
		return
	}
	var req struct {
		RootID     string   `json:"root"`
		Revision   string   `json:"revision"`
		Paths      []string `json:"paths"`
		Mode       string   `json:"mode"`
		Message    string   `json:"message"`
		SkipSafety bool     `json:"skip_safety"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	req.Revision = strings.TrimSpace(req.Revision)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	if req.Revision == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("revision required"))
		return
	}
	out, err := h.service().RestoreVersioning(r.Context(), usecase.VersioningRestoreInput{
		RootID:     req.RootID,
		Revision:   req.Revision,
		Paths:      req.Paths,
		Mode:       req.Mode,
		Message:    req.Message,
		SkipSafety: req.SkipSafety,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// timelines and tags
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningBranches(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	out, err := h.service().GetVersioningBranches(r.Context(), usecase.VersioningBranchesInput{RootID: rootID})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningBranchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID string `json:"root"`
		Name   string `json:"name"`
		From   string `json:"from"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	req.Name = strings.TrimSpace(req.Name)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("name required"))
		return
	}
	out, err := h.service().CreateVersioningBranch(r.Context(), usecase.VersioningBranchCreateInput{
		RootID: req.RootID,
		Name:   req.Name,
		From:   req.From,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningCheckout(w http.ResponseWriter, r *http.Request) {
	// Switching timelines rewrites the working tree too, and it also changes which
	// timeline the user's next snapshot lands on.
	if !h.allowAgentWorkingTreeRewrite(w, r, "switch timelines") {
		return
	}
	var req struct {
		RootID string `json:"root"`
		Name   string `json:"name"`
		Mode   string `json:"mode"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	req.Name = strings.TrimSpace(req.Name)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("name required"))
		return
	}
	out, err := h.service().CheckoutVersioningTimeline(r.Context(), usecase.VersioningCheckoutInput{
		RootID: req.RootID,
		Name:   req.Name,
		Mode:   req.Mode,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningTags(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	out, err := h.service().GetVersioningTags(r.Context(), usecase.VersioningTagsInput{RootID: rootID})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningTagCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID   string `json:"root"`
		Name     string `json:"name"`
		Revision string `json:"revision"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	req.Name = strings.TrimSpace(req.Name)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("name required"))
		return
	}
	out, err := h.service().CreateVersioningTag(r.Context(), usecase.VersioningTagCreateInput{
		RootID:   req.RootID,
		Name:     req.Name,
		Revision: req.Revision,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningTagDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID string `json:"root"`
		Name   string `json:"name"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	req.Name = strings.TrimSpace(req.Name)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("name required"))
		return
	}
	out, err := h.service().DeleteVersioningTag(r.Context(), usecase.VersioningTagDeleteInput{
		RootID: req.RootID,
		Name:   req.Name,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// maintenance
// ---------------------------------------------------------------------------

func (h *HTTPHandler) handleVersioningGC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID             string `json:"root"`
		GracePeriodSeconds int    `json:"grace_period_seconds"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	out, err := h.service().RunVersioningGC(r.Context(), usecase.VersioningGCInput{
		RootID:             req.RootID,
		GracePeriodSeconds: req.GracePeriodSeconds,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningCheckpoints(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	limit, _, err := versioningIntQuery(r, "limit")
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	out, err := h.service().ListVersioningCheckpoints(r.Context(), usecase.VersioningCheckpointsInput{
		RootID: rootID,
		Limit:  limit,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningCheckpointsPrune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RootID string `json:"root"`
		Keep   *int   `json:"keep"`
	}
	if !decodeVersioningBody(w, r, &req) {
		return
	}
	req.RootID = strings.TrimSpace(req.RootID)
	if req.RootID == "" {
		respondError(w, http.StatusBadRequest, errInvalidRequest("root required"))
		return
	}
	keep := 0
	// A pointer distinguishes "keep the default" from an explicit 0, which the
	// caller may use to drop every automatic checkpoint.
	if req.Keep != nil {
		keep = *req.Keep
		if keep < 0 {
			respondError(w, http.StatusBadRequest, errInvalidRequest("keep must not be negative"))
			return
		}
		if keep == 0 {
			keep = -1
		}
	}
	out, err := h.service().PruneVersioningCheckpoints(r.Context(), usecase.VersioningCheckpointPruneInput{
		RootID: req.RootID,
		Keep:   keep,
	})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

func (h *HTTPHandler) handleVersioningVerify(w http.ResponseWriter, r *http.Request) {
	rootID, ok := versioningRoot(w, r)
	if !ok {
		return
	}
	out, err := h.service().VerifyVersioning(r.Context(), usecase.VersioningVerifyInput{RootID: rootID})
	if err != nil {
		respondVersioningError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

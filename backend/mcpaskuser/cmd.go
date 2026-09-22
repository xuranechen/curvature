package mcpaskuser

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment variables the backend sets on the MCP server it attaches to a
// session. Everything the process needs to find its session is pinned here, so
// the agent cannot retarget the bridge at another session.
const (
	EnvEndpoint = "CURVATURE_MCP_ENDPOINT"
	EnvToken    = "CURVATURE_MCP_TOKEN"
	EnvSession  = "CURVATURE_MCP_SESSION"
	EnvAgent    = "CURVATURE_MCP_AGENT"
	EnvTimeout  = "CURVATURE_MCP_TIMEOUT"
)

// RunCLI runs the stdio MCP server for one session. Flags win over the
// environment; the environment is how the agent normally launches it.
func RunCLI(ctx context.Context, args []string) int {
	flags := flag.NewFlagSet("mcp-ask-user", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	endpoint := flags.String("endpoint", "", "Curvature backend base URL (env "+EnvEndpoint+")")
	token := flags.String("token", "", "local CLI token (env "+EnvToken+")")
	sessionKey := flags.String("session", "", "session key (env "+EnvSession+")")
	agentName := flags.String("agent", "", "agent name (env "+EnvAgent+")")
	timeout := flags.Duration("timeout", 0, "how long to wait for the user (env "+EnvTimeout+")")
	selftest := flags.Bool("selftest", false, "verify this binary can serve the bridge, then exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *selftest {
		// Used by the backend to check the command it is about to hand to an
		// agent: a command that cannot serve MCP must never be attached, because
		// the agent then fails to start its session at all.
		fmt.Printf("%s %s\n", ServerName, Version)
		return 0
	}

	server := &Server{
		SessionKey: firstNonEmpty(*sessionKey, os.Getenv(EnvSession)),
		Agent:      firstNonEmpty(*agentName, os.Getenv(EnvAgent)),
		Client: Client{
			Endpoint: firstNonEmpty(*endpoint, os.Getenv(EnvEndpoint)),
			Token:    firstNonEmpty(*token, os.Getenv(EnvToken)),
		},
		Timeout: parseTimeout(firstNonEmpty(durationString(*timeout), os.Getenv(EnvTimeout))),
		In:      os.Stdin,
		Out:     os.Stdout,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[mcp-ask-user] "+format+"\n", args...)
		},
	}
	if server.SessionKey == "" || server.Agent == "" {
		fmt.Fprintln(os.Stderr, "[mcp-ask-user] session and agent are required")
		return 2
	}
	if err := server.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "[mcp-ask-user] "+err.Error())
		return 1
	}
	return 0
}

func parseTimeout(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultTimeout
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return DefaultTimeout
	}
	if parsed > MaxTimeout {
		return MaxTimeout
	}
	return parsed
}

func durationString(value time.Duration) string {
	if value <= 0 {
		return ""
	}
	return value.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

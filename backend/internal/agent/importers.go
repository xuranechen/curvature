package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"curvature/backend/internal/agent/acp"
	"curvature/backend/internal/agent/claude"
	"curvature/backend/internal/agent/codex"
	agenttypes "curvature/backend/internal/agent/types"
)

func NewExternalSessionImporter(def Definition) (agenttypes.ExternalSessionImporter, error) {
	agentName := strings.TrimSpace(def.Name)
	if agentName == "" {
		return nil, errors.New("agent name required")
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	switch protocol {
	case ProtocolClaudeSDK:
		return claude.NewImporter(claude.ImporterOptions{
			AgentName: agentName,
		}), nil
	case ProtocolCodexSDK:
		return codex.NewImporter(codex.ImporterOptions{
			AgentName: agentName,
		}), nil
	case ProtocolACP:
		return acp.NewImporter(acp.ImporterOptions{
			AgentName: agentName,
			Command:   strings.TrimSpace(def.Command),
			Args:      append([]string{}, def.Args...),
			Env:       cloneEnv(def.Env),
			ResolveCwd: func(rootPath string) string {
				return def.ResolveCwd(rootPath)
			},
		}), nil
	default:
		return nil, errors.New("unsupported agent protocol: " + string(protocol))
	}
}

func NormalizeComparablePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	candidate := filepath.Clean(trimmed)
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil && strings.TrimSpace(resolved) != "" {
		candidate = resolved
	}
	if abs, err := filepath.Abs(candidate); err == nil {
		candidate = abs
	}
	return filepath.Clean(candidate)
}

func UserHomeDir() string {
	home, _ := os.UserHomeDir()
	return strings.TrimSpace(home)
}

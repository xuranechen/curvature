package logs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"curvature/server/internal/fs"
)

const toolCallDebugFileTpl = "sessions/%s.debug.jsonl"

type AgentLogger struct {
	enabled    bool
	root       fs.RootInfo
	sessionKey string
	mu         sync.Mutex
}

func NewAgentLogger(rootPath, sessionKey, agentName string) *AgentLogger {
	return &AgentLogger{
		enabled:    loadEnabled(rootPath, agentName) && strings.TrimSpace(sessionKey) != "",
		root:       fs.NewRootInfo("", "", rootPath),
		sessionKey: strings.TrimSpace(sessionKey),
	}
}

func loadEnabled(rootPath, agentName string) bool {
	rootPath = strings.TrimSpace(rootPath)
	agentName = strings.ToLower(strings.TrimSpace(agentName))
	if rootPath == "" || agentName == "" {
		return false
	}
	root := fs.NewRootInfo("", "", rootPath)
	raw, err := root.ReadMetaFile("debug.json")
	if err != nil {
		return false
	}
	var config map[string]bool
	if err := json.Unmarshal(raw, &config); err != nil {
		log.Printf("[agent/debuglog] config.parse.error agent=%s err=%v", agentName, err)
		return false
	}
	return config[agentName]
}

func (l *AgentLogger) AppendRaw(raw []byte) {
	if l == nil || !l.enabled {
		return
	}
	payload := bytes.TrimSpace(raw)
	if len(payload) == 0 {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := l.root.OpenMetaFileAppend(fmt.Sprintf(toolCallDebugFileTpl, l.sessionKey))
	if err != nil {
		log.Printf("[agent/debuglog] toolcall.open.error session=%s err=%v", l.sessionKey, err)
		return
	}
	defer file.Close()

	if _, err := file.Write(append(payload, '\n')); err != nil {
		log.Printf("[agent/debuglog] toolcall.write.error session=%s err=%v", l.sessionKey, err)
	}
}

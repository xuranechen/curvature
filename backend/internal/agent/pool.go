package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"curvature/backend/internal/agent/acp"
	"curvature/backend/internal/agent/claude"
	"curvature/backend/internal/agent/codex"
	agenttypes "curvature/backend/internal/agent/types"
	"curvature/backend/mcpaskuser"
)

// Pool routes agent session creation to protocol-specific runtimes.
type Pool struct {
	cfg             Config
	processCtx      context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	sessions        map[string]*sessionEntry
	runtimeEnv      map[string]map[string]string
	closed          bool
	acp             *acp.Runtime
	claude          *claude.Runtime
	codex           *codex.Runtime
	runtimeLocksMu  sync.Mutex
	runtimeLocks    map[runtimeGroup]*sync.Mutex
	memoryMu        sync.RWMutex
	memoryCollectMu sync.Mutex
	memorySnapshot  MemorySnapshot
	askUserMu       sync.RWMutex
	askUserBridge   askUserBridge
}

// askUserBridge is the loopback endpoint of the ask-user MCP server Curvature
// hands to agents that have no ask-user channel of their own.
type askUserBridge struct {
	endpoint   string
	token      string
	executable string
	// verified records that the command answered the bridge self test. An
	// unverified command is never attached: an agent that cannot start the MCP
	// server it was given fails to open its session at all, so a bad command
	// would break the agent instead of just losing the tool.
	verified bool
}

// askUserBridgeConfig is the bridge value published at startup.
func (b askUserBridge) configured() bool {
	return b.verified && b.endpoint != "" && b.token != "" && b.executable != ""
}

const (
	// EnvMCPAskUser controls the ask-user bridge from the environment. "0",
	// "false", "off" or "no" disable it; any other non-empty value names the
	// executable to launch (useful when the binary serving MCP is not the one
	// running the backend).
	EnvMCPAskUser = "CURVATURE_MCP_ASK_USER"
	// askUserVerifyTimeout bounds the self test so a hung command can never
	// stall startup.
	askUserVerifyTimeout = 5 * time.Second
)

// verifyAskUserCommand checks that a command can actually serve the bridge. It
// is a variable so tests can stub the process spawn.
var verifyAskUserCommand = defaultVerifyAskUserCommand

func defaultVerifyAskUserCommand(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, askUserVerifyTimeout)
	defer cancel()
	probe := exec.CommandContext(ctx, command, "mcp-ask-user", "--selftest")
	output, err := probe.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s mcp-ask-user --selftest: %w (%s)", command, err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), mcpaskuser.ServerName) {
		return fmt.Errorf("%s does not answer the bridge self test: %s", command, strings.TrimSpace(string(output)))
	}
	return nil
}

// MCPAskUserBridgeOptions configures the ask-user bridge. Command is the
// executable the agent launches as its MCP server; empty means this process,
// which is the normal case because the server is the same binary.
type MCPAskUserBridgeOptions struct {
	Addr    string
	Token   string
	Command string
}

// ConfigureMCPAskUserBridge publishes the endpoint agent sessions may call back
// on. It must be called before the first session is opened; sessions opened
// earlier simply do not get the tool.
//
// The command is self-tested here. If it cannot serve the bridge, the bridge is
// left disabled and the reason is logged: handing an agent an MCP server that
// does not speak the protocol makes session/new fail, which would stop that
// agent from starting at all.
func (p *Pool) ConfigureMCPAskUserBridge(opts MCPAskUserBridgeOptions) {
	if p == nil {
		return
	}
	bridge := askUserBridge{
		token:    strings.TrimSpace(opts.Token),
		endpoint: loopbackEndpoint(opts.Addr),
	}
	bridge.executable = resolveAskUserCommand(opts.Command)
	if disabledByEnv() {
		log.Printf("[mcp-ask-user] bridge disabled by %s", EnvMCPAskUser)
		p.askUserMu.Lock()
		p.askUserBridge = askUserBridge{}
		p.askUserMu.Unlock()
		return
	}
	if bridge.executable == "" {
		log.Printf("[mcp-ask-user] bridge disabled: no executable to launch as the MCP server")
		p.askUserMu.Lock()
		p.askUserBridge = askUserBridge{}
		p.askUserMu.Unlock()
		return
	}
	if bridge.endpoint == "" || bridge.token == "" {
		log.Printf("[mcp-ask-user] bridge disabled: incomplete endpoint configuration")
		p.askUserMu.Lock()
		p.askUserBridge = askUserBridge{}
		p.askUserMu.Unlock()
		return
	}
	if err := verifyAskUserCommand(context.Background(), bridge.executable); err != nil {
		log.Printf("[mcp-ask-user] bridge disabled: %v", err)
		p.askUserMu.Lock()
		p.askUserBridge = askUserBridge{}
		p.askUserMu.Unlock()
		return
	}
	bridge.verified = true
	log.Printf("[mcp-ask-user] bridge ready command=%s endpoint=%s", bridge.executable, bridge.endpoint)
	p.askUserMu.Lock()
	p.askUserBridge = bridge
	p.askUserMu.Unlock()
}

// resolveAskUserCommand decides which executable serves MCP: an explicit
// option, then the environment, then this process.
func resolveAskUserCommand(configured string) string {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		return trimmed
	}
	if fromEnv := strings.TrimSpace(os.Getenv(EnvMCPAskUser)); fromEnv != "" && !isDisabledValue(fromEnv) {
		return fromEnv
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(executable)
}

func disabledByEnv() bool {
	return isDisabledValue(os.Getenv(EnvMCPAskUser))
}

func isDisabledValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "off", "no", "disable", "disabled":
		return true
	}
	return false
}

// askUserSpec builds the MCP server entry for one session, or nil when the
// agent did not opt in or the bridge was never configured.
func (p *Pool) askUserSpec(def Definition, in agenttypes.OpenSessionInput) []acp.MCPServerSpec {
	if p == nil || !def.MCPAskUser {
		return nil
	}
	// Internal sessions (title generation and similar) and probes have no human
	// in them, so a question asked there could never be answered.
	if in.Internal || in.Probe {
		return nil
	}
	p.askUserMu.RLock()
	bridge := p.askUserBridge
	p.askUserMu.RUnlock()
	if !bridge.configured() {
		log.Printf("[mcp-ask-user] skipped session=%s agent=%s reason=bridge_not_ready", in.SessionKey, def.Name)
		return nil
	}
	log.Printf("[mcp-ask-user] attach session=%s agent=%s command=%s", in.SessionKey, def.Name, bridge.executable)
	return []acp.MCPServerSpec{{
		Name:    mcpaskuser.ServerName,
		Command: bridge.executable,
		Args:    []string{"mcp-ask-user"},
		Env: map[string]string{
			mcpaskuser.EnvEndpoint: bridge.endpoint,
			mcpaskuser.EnvToken:    bridge.token,
			// in.SessionKey is the key the pool stores this session under, so the
			// backend can resolve the bridge call with a verbatim pool lookup.
			mcpaskuser.EnvSession: in.SessionKey,
			mcpaskuser.EnvAgent:   in.AgentName,
		},
	}}
}

// loopbackEndpoint turns a listen address into the URL the bridge calls. It
// fails closed: an address it cannot read a port from yields no endpoint, so the
// agent simply does not get the tool instead of getting an unreachable one.
func loopbackEndpoint(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if !strings.HasPrefix(addr, ":") {
			return ""
		}
		host, port = "", strings.TrimPrefix(addr, ":")
	}
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	port = strings.TrimSpace(port)
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

type sessionEntry struct {
	agentName  string
	sessionKey string
	protocol   Protocol
	session    agenttypes.Session
	lastUsedAt time.Time
	activeUses int
	closing    bool
	closeDone  chan struct{}
}

// NewPool creates a new agent pool.
func NewPool(cfg Config) *Pool {
	processCtx, cancel := context.WithCancel(context.Background())
	return &Pool{
		cfg:          cfg,
		processCtx:   processCtx,
		cancel:       cancel,
		sessions:     make(map[string]*sessionEntry),
		runtimeEnv:   make(map[string]map[string]string),
		acp:          acp.NewRuntime(processCtx),
		claude:       claude.NewRuntime(),
		codex:        codex.NewRuntime(),
		runtimeLocks: make(map[runtimeGroup]*sync.Mutex),
	}
}

// SupportsDeveloperInstructions reports whether the configured transport can
// carry Curvature instructions outside the user-message history.
func (p *Pool) SupportsDeveloperInstructions(agentName string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	def, ok := p.cfg.GetAgent(strings.TrimSpace(agentName))
	if !ok {
		return false
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	return protocol == ProtocolCodexSDK || protocol == ProtocolClaudeSDK
}

// GetOrCreate returns an existing session handle or creates a new one.
func (p *Pool) GetOrCreate(ctx context.Context, in agenttypes.OpenSessionInput) (agenttypes.Session, error) {
	if in.SessionKey == "" {
		return nil, errors.New("session key required")
	}

	p.mu.Lock()
retryExisting:
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("agent pool closed")
	}
	if entry, ok := p.sessions[in.SessionKey]; ok {
		if entry.closing {
			done := entry.closeDone
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
			}
			p.mu.Lock()
			goto retryExisting
		}
		entry.lastUsedAt = time.Now()
		p.mu.Unlock()
		return entry.session, nil
	}
	def, ok := p.cfg.GetAgent(in.AgentName)
	if !ok {
		p.mu.Unlock()
		return nil, errors.New("agent not configured: " + in.AgentName)
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(in.AgentName)
	}
	p.mu.Unlock()
	unlockRuntime := p.lockRuntimeGroups([]runtimeGroup{{agentName: in.AgentName, protocol: protocol}})
	defer unlockRuntime()

	// A concurrent opener may have created the session while this goroutine
	// waited for the shared runtime lock.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("agent pool closed")
	}
	if entry, ok := p.sessions[in.SessionKey]; ok && entry != nil && entry.session != nil {
		entry.lastUsedAt = time.Now()
		existing := entry.session
		p.mu.Unlock()
		return existing, nil
	}
	p.mu.Unlock()

	// openSession starts subprocesses and can be slow, so keep it outside the pool lock.
	sess, err := p.openSession(ctx, protocol, def, in)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = sess.Close()
		return nil, errors.New("agent pool closed")
	}
	// Another goroutine may have created the same session while the lock was released.
	if entry, ok := p.sessions[in.SessionKey]; ok {
		existing := entry.session
		entry.lastUsedAt = time.Now()
		p.mu.Unlock()
		if protocol == ProtocolClaudeSDK {
			_ = sess.Close()
		}
		return existing, nil
	}
	p.sessions[in.SessionKey] = &sessionEntry{
		agentName:  in.AgentName,
		sessionKey: in.SessionKey,
		protocol:   protocol,
		session:    sess,
		lastUsedAt: time.Now(),
	}
	p.mu.Unlock()
	return sess, nil
}

// BeginSessionUse prevents idle cleanup while a turn is using the session.
// The returned function must be called when that use finishes.
func (p *Pool) BeginSessionUse(sessionKey string) func() {
	p.mu.Lock()
	entry := p.sessions[sessionKey]
	if entry == nil || entry.closing {
		p.mu.Unlock()
		return func() {}
	}
	entry.activeUses++
	entry.lastUsedAt = time.Now()
	p.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if current := p.sessions[sessionKey]; current == entry {
				if current.activeUses > 0 {
					current.activeUses--
				}
				current.lastUsedAt = time.Now()
			}
			p.mu.Unlock()
		})
	}
}

// ReleaseIdleSessions closes inactive runtime sessions without changing the
// persisted Curvature session or its closed_at metadata.
type IdleReleaseResult struct {
	ReleasedSessions int `json:"released_sessions"`
	FailedSessions   int `json:"failed_sessions"`
}

func (p *Pool) ReleaseIdleSessions(idleFor time.Duration, now time.Time) int {
	return p.ReleaseIdleSessionsDetailed(idleFor, now).ReleasedSessions
}

func (p *Pool) ReleaseIdleSessionsDetailed(idleFor time.Duration, now time.Time) IdleReleaseResult {
	if idleFor <= 0 {
		return IdleReleaseResult{}
	}
	cutoff := now.Add(-idleFor)
	return p.releaseSessionsDetailed(now, "idle_for="+idleFor.String(), func(entry *sessionEntry) bool {
		return !entry.lastUsedAt.After(cutoff)
	})
}

// ReleaseInactiveSessionsDetailed immediately releases every session that is
// not currently in use. It is intended for an explicit user action; automatic
// cleanup should continue to use ReleaseIdleSessionsDetailed with a threshold.
func (p *Pool) ReleaseInactiveSessionsDetailed(now time.Time) IdleReleaseResult {
	return p.releaseSessionsDetailed(now, "manual", func(_ *sessionEntry) bool { return true })
}

// AgentReleaseResult reports the outcome of releasing one agent on demand.
type AgentReleaseResult struct {
	Agent            string `json:"agent"`
	ReleasedSessions int    `json:"released_sessions"`
	FailedSessions   int    `json:"failed_sessions"`
	// ProcessReleased reports whether the agent's shared runtime process was
	// recycled. Claude SDK sessions own their CLI process, so they report false;
	// their process dies with the session and is already counted as released.
	ProcessReleased bool `json:"process_released"`
}

// ReleaseAgentDetailed releases every inactive session of a single agent on
// demand. Unlike the idle sweep it ignores the idle threshold, because an
// explicit user action should not have to wait for one.
//
// When the agent is left with no sessions at all, its shared runtime process is
// recycled too, so the release actually returns memory instead of only
// bookkeeping. The next message lazily opens a fresh process and resumes the
// stored agent session, so nothing is lost.
func (p *Pool) ReleaseAgentDetailed(agentName string, now time.Time) (AgentReleaseResult, error) {
	agentName = strings.TrimSpace(agentName)
	result := AgentReleaseResult{Agent: agentName}
	if agentName == "" {
		return result, errors.New("agent required")
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return result, errors.New("agent pool closed")
	}
	def, ok := p.cfg.GetAgent(agentName)
	if !ok {
		return result, errors.New("agent not configured: " + agentName)
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	group := runtimeGroup{agentName: agentName, protocol: protocol}
	unlockRuntimes := p.lockRuntimeGroups([]runtimeGroup{group})
	defer unlockRuntimes()

	released, _ := p.releaseLocked(now, "agent="+agentName, func(entry *sessionEntry) bool {
		return entry.agentName == agentName && entry.protocol == protocol
	})
	result.ReleasedSessions = released.ReleasedSessions
	result.FailedSessions = released.FailedSessions
	result.ProcessReleased = p.recycleRuntimeGroupLocked(group)
	return result, nil
}

func (p *Pool) releaseSessionsDetailed(now time.Time, reason string, eligible func(*sessionEntry) bool) IdleReleaseResult {
	groups := p.eligibleRuntimeGroups(eligible)
	if len(groups) == 0 {
		return IdleReleaseResult{}
	}
	unlockRuntimes := p.lockRuntimeGroups(groups)
	defer unlockRuntimes()

	result, touched := p.releaseLocked(now, reason, eligible)
	// A release that leaves an agent with no sessions should also return the
	// agent's process memory, not just its session bookkeeping.
	p.recycleRuntimeGroupsLocked(touched)
	return result
}

// eligibleRuntimeGroups lists the runtime groups holding at least one releasable
// session, so the caller can lock exactly those groups.
func (p *Pool) eligibleRuntimeGroups(eligible func(*sessionEntry) bool) []runtimeGroup {
	seen := make(map[runtimeGroup]struct{})
	p.mu.Lock()
	for _, entry := range p.sessions {
		if entry == nil || entry.closing || entry.activeUses > 0 || !eligible(entry) {
			continue
		}
		seen[runtimeGroup{agentName: entry.agentName, protocol: entry.protocol}] = struct{}{}
	}
	p.mu.Unlock()
	groups := make([]runtimeGroup, 0, len(seen))
	for group := range seen {
		groups = append(groups, group)
	}
	return groups
}

// releaseLocked closes every releasable session matched by eligible. The caller
// must hold the runtime lock for each group it can match and keep holding it
// until any runtime process has been recycled.
func (p *Pool) releaseLocked(now time.Time, reason string, eligible func(*sessionEntry) bool) (IdleReleaseResult, map[runtimeGroup]struct{}) {
	result := IdleReleaseResult{}
	touched := make(map[runtimeGroup]struct{})

	p.mu.Lock()
	entries := make([]*sessionEntry, 0)
	for _, entry := range p.sessions {
		if entry == nil || entry.closing || entry.activeUses > 0 || !eligible(entry) {
			continue
		}
		entry.closing = true
		entry.closeDone = make(chan struct{})
		entries = append(entries, entry)
		touched[runtimeGroup{agentName: entry.agentName, protocol: entry.protocol}] = struct{}{}
	}
	p.mu.Unlock()

	for _, entry := range entries {
		err := entry.session.Close()
		p.mu.Lock()
		current := p.sessions[entry.sessionKey]
		if current == entry {
			if err == nil {
				delete(p.sessions, entry.sessionKey)
				result.ReleasedSessions++
			} else {
				entry.closing = false
				entry.lastUsedAt = now
				result.FailedSessions++
			}
		}
		close(entry.closeDone)
		p.mu.Unlock()
		if err != nil {
			log.Printf("[agent/pool] idle_release.error session=%s agent=%s protocol=%s reason=%s err=%v", entry.sessionKey, entry.agentName, entry.protocol, reason, err)
		} else {
			log.Printf("[agent/pool] idle_release.done session=%s agent=%s protocol=%s reason=%s", entry.sessionKey, entry.agentName, entry.protocol, reason)
		}
	}
	return result, touched
}

// recycleRuntimeGroupsLocked closes the shared runtime process of every group
// that has no sessions left. The caller must hold the runtime locks.
func (p *Pool) recycleRuntimeGroupsLocked(groups map[runtimeGroup]struct{}) {
	for group := range groups {
		p.recycleRuntimeGroupLocked(group)
	}
}

// recycleRuntimeGroupLocked recycles one agent's shared runtime process when
// that agent has no sessions left. It reports whether a process was actually
// closed.
func (p *Pool) recycleRuntimeGroupLocked(group runtimeGroup) bool {
	if p.agentHasSessions(group.agentName) {
		return false
	}
	switch group.protocol {
	case ProtocolACP:
		if p.acp == nil {
			return false
		}
		if _, running := p.acp.ProcessIDs()[group.agentName]; !running {
			return false
		}
		if err := p.acp.Close(group.agentName); err != nil {
			log.Printf("[agent/pool] runtime_release.error agent=%s protocol=%s err=%v", group.agentName, group.protocol, err)
			return false
		}
	case ProtocolCodexSDK:
		if p.codex == nil {
			return false
		}
		if _, running := p.codex.ProcessIDs()[group.agentName]; !running {
			return false
		}
		if err := p.codex.Close(group.agentName); err != nil {
			log.Printf("[agent/pool] runtime_release.error agent=%s protocol=%s err=%v", group.agentName, group.protocol, err)
			return false
		}
	default:
		// Claude SDK sessions own their CLI process, so closing the session is
		// already the release; there is no shared runtime process to recycle.
		return false
	}
	log.Printf("[agent/pool] runtime_release.done agent=%s protocol=%s", group.agentName, group.protocol)
	return true
}

// agentHasSessions reports whether any session, of any protocol, still belongs
// to the agent.
func (p *Pool) agentHasSessions(agentName string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entry := range p.sessions {
		if entry != nil && entry.agentName == agentName {
			return true
		}
	}
	return false
}

type runtimeGroup struct {
	agentName string
	protocol  Protocol
}

func (p *Pool) lockRuntimeGroups(groups []runtimeGroup) func() {
	if len(groups) == 0 {
		return func() {}
	}
	unique := make(map[runtimeGroup]struct{}, len(groups))
	ordered := make([]runtimeGroup, 0, len(groups))
	for _, group := range groups {
		if _, ok := unique[group]; ok {
			continue
		}
		unique[group] = struct{}{}
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].protocol != ordered[j].protocol {
			return ordered[i].protocol < ordered[j].protocol
		}
		return ordered[i].agentName < ordered[j].agentName
	})
	locks := make([]*sync.Mutex, 0, len(ordered))
	p.runtimeLocksMu.Lock()
	for _, group := range ordered {
		lock := p.runtimeLocks[group]
		if lock == nil {
			lock = &sync.Mutex{}
			p.runtimeLocks[group] = lock
		}
		locks = append(locks, lock)
	}
	p.runtimeLocksMu.Unlock()
	for _, lock := range locks {
		lock.Lock()
	}
	return func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index].Unlock()
		}
	}
}

func (p *Pool) StartIdleReleaseLoop(ctx context.Context, idleFor func() time.Duration) {
	if p == nil || idleFor == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				p.ReleaseIdleSessions(idleFor(), now)
			}
		}
	}()
}

func (p *Pool) openSession(ctx context.Context, protocol Protocol, def Definition, in agenttypes.OpenSessionInput) (agenttypes.Session, error) {
	switch protocol {
	case ProtocolClaudeSDK:
		return p.claude.OpenSession(ctx, claude.OpenOptions{
			AgentName:             in.AgentName,
			SessionKey:            in.SessionKey,
			Model:                 in.Model,
			Effort:                in.Effort,
			PlanMode:              in.PlanMode,
			RootPath:              in.RootPath,
			Command:               def.Command,
			Args:                  append([]string{}, def.Args...),
			Env:                   cloneEnv(def.Env),
			DeveloperInstructions: in.DeveloperInstructions,
			ResumeSessionID:       in.AgentSessionID,
			ForkSessionID:         in.ForkPoint.AgentSessionID,
			ResumeMessageID:       in.ForkPoint.ClaudeMessageUUID,
		})
	case ProtocolCodexSDK:
		var codexUserOrdinal *int
		if in.ForkPoint.Kind == agenttypes.ForkPointCodexUserOrdinal {
			value := in.ForkPoint.CodexUserOrdinal
			codexUserOrdinal = &value
		}
		return p.codex.OpenSession(ctx, codex.OpenOptions{
			AgentName:             in.AgentName,
			SessionKey:            in.SessionKey,
			Model:                 in.Model,
			Effort:                in.Effort,
			FastService:           in.FastService,
			PlanMode:              in.PlanMode,
			Probe:                 in.Probe,
			RootPath:              in.RootPath,
			Command:               def.Command,
			Args:                  append([]string{}, def.Args...),
			Env:                   cloneEnv(def.Env),
			DeveloperInstructions: in.DeveloperInstructions,
			ResumeSessionID:       in.AgentSessionID,
			ForkSessionID:         in.ForkPoint.AgentSessionID,
			CodexUserOrdinal:      codexUserOrdinal,
		})
	case ProtocolACP:
		fallthrough
	default:
		return p.acp.OpenSession(ctx, acp.OpenOptions{
			AgentName:       in.AgentName,
			SessionKey:      in.SessionKey,
			Model:           in.Model,
			Mode:            in.Mode,
			Effort:          in.Effort,
			RootPath:        in.RootPath,
			Command:         def.Command,
			Args:            def.BuildArgs(in.RootPath),
			Env:             cloneEnv(def.Env),
			Cwd:             def.ResolveCwd(in.RootPath),
			ResumeSessionID: in.AgentSessionID,
			MCPServers:      p.askUserSpec(def, in),
		})
	}
}

func (p *Pool) KillAgentProcess(agentName string, wait time.Duration) (string, bool) {
	_ = wait
	def, ok := p.cfg.GetAgent(agentName)
	if !ok {
		return "", false
	}

	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	unlockRuntime := p.lockRuntimeGroups([]runtimeGroup{{agentName: agentName, protocol: protocol}})
	defer unlockRuntime()
	switch protocol {
	case ProtocolClaudeSDK:
		p.closeSessionsForAgent(agentName, ProtocolClaudeSDK)
		log.Printf("[agent/pool] kill_agent_process.claude_closed agent=%s", agentName)
		return "", true
	case ProtocolCodexSDK:
		p.closeSessionsForAgent(agentName, ProtocolCodexSDK)
		_ = p.codex.Close(agentName)
		log.Printf("[agent/pool] kill_agent_process.codex_closed agent=%s", agentName)
		return "", true
	case ProtocolACP:
		p.closeSessionsForAgent(agentName, ProtocolACP)
		_ = p.acp.Close(agentName)
		if hint, ok := p.acp.RecentCloseHint(agentName); ok {
			log.Printf("[agent/pool] kill_agent_process.hint agent=%s hint=%q", agentName, hint)
			return hint, true
		}
		log.Printf("[agent/pool] kill_agent_process.no_hint agent=%s", agentName)
		return "", false
	default:
		return "", false
	}
}

func (p *Pool) closeSessionsForAgent(agentName string, protocol Protocol) {
	p.closeSessions(
		p.takeSessions(func(entry *sessionEntry) bool {
			return entry.agentName == agentName && entry.protocol == protocol
		}),
	)
}

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}

// Close closes a session (not the underlying runtime pool).
func (p *Pool) Close(sessionKey string) {
	p.mu.Lock()
	groups := make([]runtimeGroup, 0, 1)
	if entry := p.sessions[sessionKey]; entry != nil {
		groups = append(groups, runtimeGroup{agentName: entry.agentName, protocol: entry.protocol})
	}
	p.mu.Unlock()
	unlockRuntimes := p.lockRuntimeGroups(groups)
	defer unlockRuntimes()
	entries := p.takeSessions(func(entry *sessionEntry) bool {
		return entry.sessionKey == sessionKey
	})
	if len(entries) == 0 {
		return
	}
	p.closeSessions(entries)
}

func (p *Pool) takeSessions(match func(*sessionEntry) bool) []*sessionEntry {
	p.mu.Lock()
	defer p.mu.Unlock()

	var entries []*sessionEntry
	for key, entry := range p.sessions {
		if entry == nil || !match(entry) {
			continue
		}
		entries = append(entries, entry)
		delete(p.sessions, key)
	}
	return entries
}

func (p *Pool) closeSessions(entries []*sessionEntry) {
	for _, entry := range entries {
		if entry == nil || entry.session == nil {
			continue
		}
		_ = entry.session.Close()
	}
}

// Config returns the pool configuration.
func (p *Pool) Config() Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

func (p *Pool) CodexRateLimits(ctx context.Context, agentName string) (codex.RateLimitStatus, error) {
	opts, err := p.codexRuntimeOptions(agentName)
	if err != nil {
		return codex.RateLimitStatus{}, err
	}
	return p.codex.ReadRateLimits(ctx, opts)
}

func (p *Pool) ConsumeCodexRateLimitReset(ctx context.Context, agentName, idempotencyKey, creditID string) (codex.ConsumeRateLimitResetResult, error) {
	opts, err := p.codexRuntimeOptions(agentName)
	if err != nil {
		return codex.ConsumeRateLimitResetResult{}, err
	}
	return p.codex.ConsumeRateLimitReset(ctx, opts, idempotencyKey, creditID)
}

func (p *Pool) codexRuntimeOptions(agentName string) (codex.OpenOptions, error) {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return codex.OpenOptions{}, errors.New("agent required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return codex.OpenOptions{}, errors.New("agent pool closed")
	}
	def, ok := p.cfg.GetAgent(agentName)
	if !ok {
		return codex.OpenOptions{}, errors.New("agent not configured: " + agentName)
	}
	protocol := def.Protocol
	if protocol == "" {
		protocol = DefaultProtocol(agentName)
	}
	if protocol != ProtocolCodexSDK {
		return codex.OpenOptions{}, errors.New("agent does not use codex-sdk: " + agentName)
	}
	return codex.OpenOptions{
		AgentName: agentName,
		Command:   def.Command,
		Args:      append([]string(nil), def.Args...),
		Env:       cloneEnv(def.Env),
	}, nil
}

func (p *Pool) UpdateConfig(cfg Config) Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg = p.applyRuntimeEnvOverridesLocked(cfg)
	p.cfg = cfg
	return p.cfg
}

func (p *Pool) SetAgentEnv(agentName string, env map[string]string) error {
	if agentName == "" {
		return errors.New("agent required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.cfg.Agents {
		if p.cfg.Agents[i].Name != agentName {
			continue
		}
		p.runtimeEnv[agentName] = cloneEnv(env)
		p.cfg.Agents[i].Env = cloneEnv(env)
		return nil
	}
	return errors.New("agent not configured: " + agentName)
}

func (p *Pool) applyRuntimeEnvOverridesLocked(cfg Config) Config {
	if len(p.runtimeEnv) == 0 {
		return cfg
	}
	for i := range cfg.Agents {
		env, ok := p.runtimeEnv[cfg.Agents[i].Name]
		if !ok {
			continue
		}
		cfg.Agents[i].Env = cloneEnv(env)
	}
	return cfg
}

// Get returns an existing session handle if present.
func (p *Pool) Get(sessionKey string) (agenttypes.Session, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.sessions[sessionKey]
	if !ok || entry == nil || entry.session == nil {
		return nil, false
	}
	return entry.session, true
}

// SessionKeys returns the keys of the sessions currently held by the pool,
// sorted. It exists so lookups that fail (for example a bridge call naming an
// unknown session) can report what the pool actually has.
func (p *Pool) SessionKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(p.sessions))
	for key := range p.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Context returns the pool lifecycle context (read-only).
func (p *Pool) Context() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.processCtx != nil {
		return p.processCtx
	}
	return context.Background()
}

// CloseAll closes all runtime resources.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	p.closed = true
	cancel := p.cancel
	p.cancel = nil
	groups := make([]runtimeGroup, 0, len(p.cfg.Agents))
	for _, def := range p.cfg.Agents {
		protocol := def.Protocol
		if protocol == "" {
			protocol = DefaultProtocol(def.Name)
		}
		groups = append(groups, runtimeGroup{agentName: def.Name, protocol: protocol})
	}
	p.mu.Unlock()

	// Cancel the process lifecycle before waiting for runtime locks. An ACP
	// process may still be initializing while holding one of those locks; its
	// command and initialize RPC both use this context.
	if cancel != nil {
		cancel()
	}

	unlockRuntimes := p.lockRuntimeGroups(groups)
	defer unlockRuntimes()

	p.mu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*sessionEntry)
	acpRuntime := p.acp
	claudeRuntime := p.claude
	codexRuntime := p.codex
	p.mu.Unlock()

	// Claude SDK sessions own independent CLI processes rather than a shared
	// runtime, so each resident session must be closed explicitly. Close them
	// concurrently so shutdown is bounded by one SDK grace period.
	var claudeCloseWG sync.WaitGroup
	for _, entry := range sessions {
		if entry == nil || entry.protocol != ProtocolClaudeSDK || entry.session == nil {
			continue
		}
		claudeCloseWG.Add(1)
		go func(entry *sessionEntry) {
			defer claudeCloseWG.Done()
			if err := entry.session.Close(); err != nil {
				log.Printf("[agent/pool] close_all.claude_error session=%s agent=%s err=%v", entry.sessionKey, entry.agentName, err)
			}
		}(entry)
	}
	claudeCloseWG.Wait()

	if acpRuntime != nil {
		acpRuntime.CloseAll()
	}
	if claudeRuntime != nil {
		claudeRuntime.CloseAll()
	}
	if codexRuntime != nil {
		codexRuntime.CloseAll()
	}
}

# Curvature

> AI Agent Remote Access Gateway

Access your personal AI agents and workstation data anywhere, anytime.

---

## Features

### Agent Sessions
- **Multi-Agent**: Claude Code · OpenAI Codex · Gemini CLI · Grok · Cursor · Copilot · CodeBuddy · Cline · Augment · Kiro · Kimi · Qwen · Qoder · OMP · Pi · Hermes · DSH · Reasonix · OpenCode · OpenClaw — installed agents detected automatically.
- **Real-time streaming**: Token-by-token output, tool calls, thought traces, and permission prompts rendered live.
- **Flexible switching**: Switch agents or models mid-session; all agents share the same context.
- **Session search & external import**: Search by title or content; import sessions from agent CLIs and continue natively, with bidirectional sync.
- **Configuration management**: Backup / add API providers and switch with one click across multiple accounts.
- **Subagents**: Codex / Claude Code subagents auto-discovered and displayed.
- **Scheduled tasks**: Trigger agents to run at specified times.

### Task Board
- **Concurrent execution** with per-task worktree isolation.
- **Task templates** with customizable stages (agent, model, planning mode, preset prompts).
- **Deep linking** between tasks, worktrees, sessions, and files.

### File Access
- **Multiple projects**: Manage several directories independently.
- **Self-hosted data**: Stored in `.curvature/` per project or `~/.curvature/<rootId>/`.
- **File tree browser**: Full directory navigation, Markdown / image / code renderers, git status & worktree support.

### Interaction
- `/` slash commands, `@` file references, `#` prompt shortcuts.
- **Bidirectional file-session linking**: Jump from file to session, or session to related files.
- **PWA support**: Install to desktop or mobile home screen.
- **Mobile-optimized UI** with bottom action bar and keyboard adaptation.
- **Web Push notifications** (iOS requires adding to home screen).
- **User habits**: Swap sidebars, switch between single/multi-project session lists.

### Access Modes
- **Local mode**: Accessible immediately in browser after startup — no configuration needed.
- **Relay (self-hosted)**: Access from anywhere via a self-hosted relay server (Docker supported) — no firewall ports, no third-party dependency.
- **Private network**: Access directly via `ip:port` through Tailscale or similar.
- **End-to-end encryption**: Protect sessions and files with E2EE + pairing code.

### Plugin System
- Custom file views: receive file → parse → render UI.
- Agent-generated plugins with interactive action buttons.

### Command Execution
- Card output, history suggestions, screen-width adaptation, selectable shell, session-persistent shells.

### Remote Access to Local Services
- **One-click exposure**: In relay mode, configure a local service address and expose it to the public network with a unique subdomain.

---

## Quick Start

### Prerequisites

Curvature does not include AI models — you need at least one Agent CLI installed locally:

| Agent | Install |
|-------|---------|
| **Claude Code** | https://code.claude.com/docs/en/quickstart |
| **OpenAI Codex** | https://developers.openai.com/codex/cli |
| **Gemini CLI** | https://geminicli.com/ |
| **Cursor** | https://cursor.com/cn/cli |
| **GitHub Copilot** | https://github.com/features/copilot/cli |
| **CodeBuddy** | https://www.codebuddy.ai/docs/cli/installation (`codebuddy --acp`) |
| **Cline** | https://cline.bot/kanban |
| **Augment** | https://www.augmentcode.com/product/CLI |
| **Kiro** | https://kiro.dev/cli/ |
| **OpenCode** | https://opencode.ai/ |
| **OpenClaw** | https://docs.openclaw.ai/ |
| **Kimi** | https://www.kimi.com/code/docs/kimi-cli/guides/getting-started.html |
| **Qwen** | https://qwen.ai/qwencode |
| **Qoder** | https://docs.qoder.com/cli/quick-start |
| **OMP** | https://github.com/can1357/oh-my-pi (`omp acp`) |
| **Pi** | https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent + [pi-acp](https://github.com/svkozak/pi-acp) |
| **Hermes** | https://hermes-agent.nousresearch.com/docs/user-guide/features/acp |
| **DSH** | https://github.com/deepseek-ai/deepseek-harness + [acp adapter](https://github.com/openma-ai/deepseek-harness-acp) |
| **Reasonix** | https://github.com/esengine/DeepSeek-Reasonix |
| **Grok Build** | https://x.ai/cli |

Once an agent is installed, start Curvature and interact with it through the browser.

### Install

**From source** (requires Go 1.22+, Node.js 20+)
```bash
git clone https://github.com/your-org/curvature.git
cd curvature
make build      # output: ./curvature
```

**macOS / Linux**
```bash
curl -fsSL https://raw.githubusercontent.com/your-org/curvature/main/scripts/install.sh | bash
```

**Windows (PowerShell)**
```powershell
irm https://raw.githubusercontent.com/your-org/curvature/main/scripts/install.ps1 | iex
```

Or download the latest release directly from [GitHub Releases](https://github.com/your-org/curvature/releases).

### Run

```bash
curvature                           # manage current directory
curvature /path/to/project          # manage a specific directory
curvature -addr :9000 /path         # custom port
```

Open [http://localhost:7331](http://localhost:7331) in your browser.

#### HTTPS (TLS)

```bash
curvature -tls
curvature -tls -cert /path/to/cert.pem -key /path/to/key.pem
```

---

## Relay (Self-hosted Remote Access)

Run a relay server on your VPS to access Curvature from anywhere — no firewall ports, no third-party dependency.

See [`relay/README.md`](./relay/README.md) for full deployment guide (Docker or native).

**Quick start (Docker):**
```bash
# On your server
BASE_URL=https://relay.example.com docker compose up -d

# On your local machine
curvature -addr 0.0.0.0:7331
# Open Curvature UI → click "Public Access" in the bottom-left → confirm binding
# Access your node at https://relay.example.com/n/{node_id}/
```

---

## CLI Reference

```bash
curvature [flags] [root]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-addr string` | `127.0.0.1:7331` | Listen address. Use `:7331` for LAN access. |
| `-foreground` | `false` | Run in foreground instead of starting a background service. |
| `-autostart` | `false` | Register automatic startup and environment snapshot. `-autostart=false` to disable. |
| `-status` | `false` | Show background service status, PID, URL, log path. |
| `-version` | `false` | Show version. |
| `-update` | `false` | Check and install latest release. |
| `-uninstall` | `false` | Print uninstall command for current platform. |
| `-stop` / `-restart` | `false` | Stop or restart the background service. |
| `-remove` | `false` | Remove `root` from managed directory list. |
| `-config string` | empty | Read startup options from a JSON file. |
| `-agent-config string` | empty | Load an extra `agents.json` for custom ACP agents. |
| `-no-relayer` | `false` | Disable relay integration. |
| `-e2ee` | `false` | Enable end-to-end encryption. Pairing code printed on first enable. |
| `-tls` | `false` | Enable HTTPS. Auto-generates self-signed cert if no `-cert`/`-key` provided. |
| `-cert` / `-key` | empty | TLS certificate and key files (PEM). |
| `-web-push` | `true` | Enable PWA Web Push notifications. |
| `-notify-script string` | empty | Executable script for notification events (JSON on stdin). |

#### Custom ACP Agents

```json
{
  "agents": [
    {
      "name": "my-agent",
      "brief": "Short description for the install/update list.",
      "command": "my-agent",
      "protocol": "acp",
      "args": ["--acp"]
    }
  ]
}
```

Start with: `curvature -agent-config /path/to/agents.json`

---

## Contributing

Pull requests are welcome. For larger changes, please open an issue first.

## License

[AGPL v3](LICENSE)

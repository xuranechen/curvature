# Curvature

> AI Agent 远程访问网关

随时随地访问你的 AI Agent 和工作站数据。

---

## 特性

### Agent 会话
- **多 Agent 支持**：Claude Code · OpenAI Codex · Gemini CLI · Grok · Cursor · Copilot · CodeBuddy · Cline · Augment · Kiro · Kimi · Qwen · Qoder · OMP · Pi · Hermes · DSH · Reasonix · OpenCode · OpenClaw，自动探测已安装的 Agent。
- **实时流式输出**：逐 token 推送，工具调用、思考过程、权限请求均实时渲染。
- **灵活切换**：会话中随时切换 Agent 或模型，多 Agent 共享同一上下文。
- **会话搜索与外部导入**：按标题或内容搜索；可从 Agent CLI 导入已有会话并原生继续，支持双向同步。
- **配置管理**：备份 / 添加 API 供应商，多账号 / 多 API Key 一键切换。
- **subagent**：Codex / Claude Code subagent 自动发现和展示。
- **定时任务**：在指定时间触发 Agent 执行任务。

### 任务看板
- **并发执行**：任务之间通过 worktree 隔离。
- **任务模板**：自定义任务阶段（agent、模型、计划模式、预置提示词）。
- **深度关联**：任务、worktree、会话、文件之间动态关联。

### 文件访问
- **多 Project**：同时托管多个目录，会话按项目独立组织。
- **数据自托管**：存储在项目 `.curvature/` 或 `~/.curvature/<rootId>/`。
- **文件树浏览**：完整目录导航，Markdown / 图片 / 代码渲染，支持 git status 与 worktree。

### 交互优化
- `/` 斜杠命令、`@` 文件引用、`#` 快捷提示词。
- **文件与会话双向跳转**：由文件直达产生它的会话，或由会话查看所有相关文件。
- **PWA 支持**：可安装到桌面或手机主屏。
- **手机界面优化**：底部操作栏拇指可及，输入框适配软键盘。
- **通知推送**：Web Push 推送会话状态变化（iOS 需添加到主屏幕）。
- **用户习惯适配**：左右侧边栏可交换、单项目/多项目会话列表可切换。

### 访问模式
- **本地模式**：服务启动后即可在浏览器访问，无需任何配置。
- **Relay 自建中继**：通过自托管 relay 服务器（支持 Docker）随时随地访问，无需开放防火墙端口、不依赖第三方。
- **私有网络**：通过 Tailscale 等直接以 `ip:port` 访问。
- **端到端加密**：会话、文件支持 E2EE + 配对码保护。

### 插件系统
- 定制文件视图：传入文件内容 → 解析 → 渲染界面。
- Agent 生成插件，支持交互动作按钮。

### 命令执行
- 卡片输出、历史命令候选、屏幕宽度适配、可选 shell、长驻 shell 会话。

### 远程访问本地服务
- **一键暴露**：Relay 模式下配置本地服务地址，即可通过唯一子域名暴露到公网。

---

## 快速上手

### 前置条件

Curvature 本身不包含 AI 模型，需要在本机安装至少一个 Agent CLI：

| Agent | 安装 |
|-------|------|
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
| **DSH** | https://github.com/deepseek-ai/deepseek-harness + [ACP 适配器](https://github.com/openma-ai/deepseek-harness-acp) |
| **Reasonix** | https://github.com/esengine/DeepSeek-Reasonix |
| **Grok Build** | https://x.ai/cli |

安装好 Agent 后，启动 Curvature 并通过浏览器与之交互。

### 安装

**从源码编译**（需要 Go 1.22+、Node.js 20+）
```bash
git clone https://github.com/your-org/curvature.git
cd curvature
make build      # 产物为 ./curvature
```

**macOS / Linux**
```bash
curl -fsSL https://raw.githubusercontent.com/your-org/curvature/main/scripts/install.sh | bash
```

**Windows（PowerShell）**
```powershell
irm https://raw.githubusercontent.com/your-org/curvature/main/scripts/install.ps1 | iex
```

或直接从 [GitHub Releases](https://github.com/your-org/curvature/releases) 下载最新版本。

### 启动

```bash
curvature                           # 托管当前目录
curvature /path/to/project          # 托管指定目录
curvature -addr :9000 /path         # 指定端口
```

在浏览器中打开 [http://localhost:7331](http://localhost:7331)。

#### HTTPS (TLS)

```bash
curvature -tls
curvature -tls -cert /path/to/cert.pem -key /path/to/key.pem
```

---

## Relay 自建中继

在 VPS 上部署一个 relay 服务器，即可随时随地访问 Curvature——无需开放防火墙端口、不依赖第三方。

完整部署指南（Docker 或原生）见 [`relay/README.md`](./relay/README.md)。

**Docker 快速启动：**
```bash
# 服务器上
BASE_URL=https://relay.example.com docker compose up -d

# 本机
curvature -addr 0.0.0.0:7331
# 打开 Curvature UI → 左下角「从公网访问」→ 确认绑定
# 之后访问 https://relay.example.com/n/{node_id}/ 即可
```

---

## CLI 参考

```bash
curvature [flags] [root]
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-addr string` | `127.0.0.1:7331` | 监听地址。使用 `:7331` 可允许局域网访问。 |
| `-foreground` | `false` | 前台运行，不启动后台服务。 |
| `-autostart` | `false` | 注册开机自启及环境快照；`-autostart=false` 禁用。 |
| `-status` | `false` | 查看后台服务状态、PID、访问地址、日志路径。 |
| `-version` | `false` | 查看版本。 |
| `-update` | `false` | 检查并安装最新版本。 |
| `-uninstall` | `false` | 打印当前平台卸载命令。 |
| `-stop` / `-restart` | `false` | 停止或重启后台服务。 |
| `-remove` | `false` | 从托管目录列表移除 `root`。 |
| `-config string` | 空 | 从 JSON 文件读取启动参数。 |
| `-agent-config string` | 空 | 加载额外的 `agents.json`（自定义 ACP Agent）。 |
| `-no-relayer` | `false` | 禁用 Relay 集成。 |
| `-e2ee` | `false` | 启用端到端加密，首次启用时打印配对码。 |
| `-tls` | `false` | 启用 HTTPS。未指定 `-cert`/`-key` 时自动生成自签名证书。 |
| `-cert` / `-key` | 空 | TLS 证书与私钥（PEM）。 |
| `-web-push` | `true` | 启用 PWA Web Push 通知。 |
| `-notify-script string` | 空 | 通知事件脚本（stdin 传入 JSON）。 |

#### 自定义 ACP Agent

```json
{
  "agents": [
    {
      "name": "my-agent",
      "brief": "显示在安装/更新列表中的简短说明",
      "command": "my-agent",
      "protocol": "acp",
      "args": ["--acp"]
    }
  ]
}
```

启动：`curvature -agent-config /path/to/agents.json`

---

## 参与贡献

欢迎提交 Pull Request。较大的改动请先开 Issue 讨论。

## 许可证

[AGPL v3](LICENSE)
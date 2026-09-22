# Ask-user bridge (client-supplied MCP tool)

Some agents cannot ask the human a question. `dsh` is the measured example: its
`ask_user_question` tool is not exposed over ACP, and its user-question seam has
no answerer, so a question either never happens or dies with
`no user-questions answerer accepted the request`.

The bridge works around that without changing the agent. Curvature hands the
session an **MCP server of its own**; the model calls the tool that server
exposes, the server calls Curvature back on the loopback address, Curvature
renders the ordinary ask-user card, and the human's answer travels back to the
model as the tool result.

```
agent model
  │  MCP tools/call ask_user_question
  ▼
curvature mcp-ask-user            (stdio MCP server, one process per session)
  │  POST /api/mcp/ask-user  (loopback + local CLI token)
  ▼
Curvature backend
  │  emit ask_user ToolCall  ──►  ask-user card  ──►  human clicks
  ▲                                                        │
  └──────────── answer ◄──── WS session.answer_question ◄───┘
```

Because the tool belongs to the *client*, this route is protocol-agnostic: the
agent only has to support ACP's `mcpServers` field (every ACP agent does; `dsh`
advertises `mcpCapabilities`). Nothing agent-side is patched or forked.

## Turning it on

Per agent, in `agents.json`:

```json
{
  "name": "dsh",
  "protocol": "acp",
  "mcpAskUser": true
}
```

`dsh` already has it enabled in the shipped `agents.json`. Any ACP agent can opt
in the same way; agents with a working native ask-user channel (claude, codex)
do not need it.

Three conditions must hold before the tool is attached, all checked at startup:

1. **The launched binary must serve the bridge.** Curvature launches itself with
   the `mcp-ask-user` subcommand, so every entry point handles it
   (`cli/cmd/curvature.go` and `backend/cmd/curvature-server/main.go`). If the
   binary cannot serve it, worse than losing the tool: the agent cannot start the
   MCP server it was handed and `session/new` fails with
   `mcp-client(curvature-ask-user): initial connection or tool synchronization
   failed` — the agent then cannot start at all.
2. **The command passes a self test.** Before publishing the bridge, the backend
   runs `<command> mcp-ask-user --selftest` (5s cap). If that fails, the bridge is
   disabled and the reason is logged; sessions are still opened normally. This is
   why a wrong or stale binary can no longer break agent startup.
3. **The session is interactive.** Internal sessions (title generation) and
   probes are excluded: no human is watching them, so a question asked there
   could never be answered.

`CURVATURE_MCP_ASK_USER` controls this from the environment: `0`/`false`/`off`/
`no`/`disabled` disables the bridge entirely, and any other value names the
executable to launch (for layouts where the binary serving MCP is not the one
running the backend).

## Pieces

| Piece | File | Role |
| --- | --- | --- |
| MCP stdio server | `backend/mcpaskuser/protocol.go` | Speaks MCP on stdio: `initialize`, `tools/list`, `tools/call`, cancellation. Exposes only `ask_user_question`. |
| Bridge client + card | `backend/mcpaskuser/bridge.go` | POSTs the question back, builds the card, maps the answer onto the tool result. |
| CLI entry | `backend/mcpaskuser/cmd.go`, `backend/cmd/curvature-server/main.go`, `cli/cmd/curvature.go` | `curvature mcp-ask-user` (and `--selftest`), driven by `CURVATURE_MCP_*` environment variables. |
| HTTP endpoint | `backend/internal/api/http_mcp_ask_user.go` | Authenticates the caller, then delegates to the use case. |
| Use case | `backend/internal/api/usecase/mcp_ask_user.go` | Finds the live session and calls `AskUserQuestion`. |
| Session side | `backend/internal/agent/acp/session.go` | Registers the pending question, emits the card, routes the answer, closes the card. |
| Attachment | `backend/internal/agent/pool.go` | Adds the MCP server to `session/new` for agents that opt in (`ConfigureMCPAskUserBridge`; `MCPAskUserBridgeOptions.Command` overrides which executable is launched, which is how the live test avoids launching the test binary). |
| Per-session servers | `backend/internal/agent/acp/process.go` | The server list travels with each `session/new` call. It is deliberately *not* stored on the process: a process is shared by every session of an agent, and one session must not decide what another advertises. |

## Wire format

Tool input (what the model calls with):

```json
{
  "questions": [
    {
      "id": "color",
      "question": "Which color do you prefer?",
      "header": "Color",
      "options": [{ "label": "Red" }, { "label": "Blue" }],
      "multi_select": false
    }
  ]
}
```

Tool result (what the model reads back):

```json
{ "answers": [{ "id": "color", "selected": ["Blue"] }] }
```

A question without `options` is answered in the user's own words and comes back
as `{"id": "...", "custom": "typed text"}`.

Backend callback (`POST /api/mcp/ask-user`):

```json
{ "session_key": "...", "agent": "dsh", "timeout_ms": 1800000,
  "request": { "call_id": "3", "questions": [ ... ] } }
```

## Security model

- The MCP server process is started by the agent with the local CLI token and
  the loopback endpoint in its environment; the endpoint accepts only requests
  carrying that token from a loopback address (`isLocalBridgeRequest`). A remote
  caller cannot use the token even if it learns it.
- The session identity is pinned in the process environment. The agent cannot
  ask on behalf of another session, and the model cannot retarget the card.
- One server process is created per `session/new`, so concurrent sessions cannot
  cross-talk; the pending-question registry is per session and keyed by call id.
- The backend refuses to render a card it cannot answer: without a live session,
  the callback fails and the tool returns an error to the model.

## Behaviour and limits

- **Timeout.** The wait defaults to 30 minutes (`CURVATURE_MCP_TIMEOUT`, capped
  at 12 hours). On timeout the card is closed as *unanswered* and the tool
  returns `isError: true`, so the model is told not to invent an answer.
- **Cancellation.** If the agent cancels the tool call, the in-flight callback is
  cancelled and the card closes the same way.
- **Ordering.** The card id is prefixed `mcp-ask-user:` so bridged questions can
  never collide with native tool-call ids.
- **Show, don't duplicate.** A bridged question arrives as a normal
  `ask_user` tool call, so the existing card renders it. The agent's own
  tool-call update for the MCP tool (`mcp__curvature-ask-user__ask_user_question`)
  is suppressed in the ACP adapter: the card is the single representation of that
  question. Other tools from the same server, and other servers' tools, are
  untouched.
- **Free-form answers.** A question without options is answered in the user's own
  words; the card already provides the text field.
- **Agents that ask in prose** are still out of scope: if a model writes the
  question into its reply instead of calling a tool, there is nothing to card.

## Tests

Unit tests cover the tool schema, answer mapping, the stdio protocol, the
endpoint's authentication and the session-side registry:

```bash
go test ./backend/mcpaskuser/ ./backend/internal/agent/acp/ ./backend/internal/api/
```

The live test runs the whole loop through the real agent pool and the real HTTP
endpoint, replacing only the human (it clicks the first option as soon as the
card appears):

```bash
go test -tags acpintegration -count=1 -run TestLiveMCPAskUserBridge -v \
  -timeout 12m ./backend/internal/api/
```

It loads `agents.json` (so it also proves the `mcpAskUser` flag reaches the
pool), builds `curvature-server` (or uses `CURVATURE_MCP_ASK_BIN`), then asserts
that the model called the bridged tool, that the call carried the pool session
key, that the question became a card, that no duplicate tool row appeared, and
that the model used the answer. Without `dsh` on `PATH` it skips.

To check a binary by hand, drive the handshake it must answer:

```bash
# does this binary serve the bridge at all?
curvature mcp-ask-user --selftest     # prints "curvature-ask-user <version>"

# full stdio handshake: initialize -> notifications/initialized -> tools/list
node .curvature/mcp-spike/mcp-handshake.mjs <path-to-curvature.exe> mcp-ask-user
```

The same scripts drive a **real** end-to-end run without a browser, sitting in
for the web client over the real WebSocket API (the human answer is chosen by the
caller):

```bash
node .curvature/mcp-spike/drive-ask-user.mjs <port> Blue
```

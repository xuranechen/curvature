package main

import (
	"html"
	"net/http"
)

// handleBindPage serves the confirmation page at /bind?code=...
func (s *Server) handleBindPage(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/nodes", http.StatusFound)
		return
	}
	root := html.EscapeString(r.URL.Query().Get("root"))
	writeHTML(w, bindPageHTML(code, root))
}

// handleNodesPage serves /nodes
func (s *Server) handleNodesPage(w http.ResponseWriter, r *http.Request) {
	writeHTML(w, nodesPageHTML(s.base))
}

// handleDevicesList serves GET /api/devices
func (s *Server) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	devices := s.store.listDevices()
	items := make([]map[string]any, 0, len(devices))
	for _, d := range devices {
		items = append(items, map[string]any{
			"node_id":      d.NodeID,
			"node_name":    d.NodeName,
			"node_url":     s.binds.nodeURL(d.NodeID),
			"online":       s.hub.get(d.NodeID) != nil,
			"has_password": d.AccessPassword != "",
			"bound_at":     d.BoundAt,
		})
	}
	respondJSON(w, http.StatusOK, items)
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// pageStyles mirrors the Curvature web app's launcher look (web/src/components/Login.tsx
// + web/src/index.css tokens) so the self-hosted relay pages match the main page.
func pageStyles() string {
	return `<style>
  :root {
    color-scheme: light dark;
    --bg: radial-gradient(circle at top left, rgba(91, 125, 184, 0.08), transparent 24%), radial-gradient(circle at right 20%, rgba(148, 163, 184, 0.20), transparent 26%), linear-gradient(180deg, #f8fafc 0%, #edf2f7 100%);
    --card-bg: rgba(255, 255, 255, 0.82);
    --card-bg-strong: rgba(255, 255, 255, 0.94);
    --chip-bg: rgba(148, 163, 184, 0.18);
    --border: rgba(148, 163, 184, 0.2);
    --border-strong: rgba(100, 116, 139, 0.34);
    --fg: #172033;
    --muted: #5f6f86;
    --accent: #5b7db8;
    --accent-strong: #4a6ca4;
    --surface-input: rgba(255, 255, 255, 0.74);
    --shadow: 0 16px 44px rgba(15, 23, 42, 0.08);
    --shadow-card: 0 8px 30px rgba(15, 23, 42, 0.06);
    --danger: #dc2626;
    --ok: #15803d;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: radial-gradient(circle at top left, rgba(91, 125, 184, 0.12), transparent 24%), radial-gradient(circle at right 20%, rgba(139, 159, 188, 0.10), transparent 26%), linear-gradient(180deg, #0f172a 0%, #020617 100%);
      --card-bg: rgba(30, 41, 59, 0.82);
      --card-bg-strong: rgba(30, 41, 59, 0.94);
      --chip-bg: rgba(148, 163, 184, 0.16);
      --border: rgba(255, 255, 255, 0.08);
      --border-strong: rgba(255, 255, 255, 0.16);
      --fg: #F8FAFC;
      --muted: #94A3B8;
      --accent: #7d9fd0;
      --accent-strong: #93b2dd;
      --surface-input: rgba(15, 23, 42, 0.6);
      --shadow: 0 16px 44px rgba(0, 0, 0, 0.4);
      --shadow-card: 0 8px 30px rgba(0, 0, 0, 0.35);
    }
  }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: system-ui, -apple-system, sans-serif; min-height: 100vh; background: var(--bg); color: var(--fg); -webkit-font-smoothing: antialiased; }
  .center { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 16px; }
  .wrap { max-width: 720px; margin: 0 auto; padding: 28px 20px 48px; }
  .card { width: 400px; max-width: 100%; background: var(--card-bg); border: 1px solid var(--border); border-radius: 22px; padding: 30px 26px; box-shadow: var(--shadow); backdrop-filter: blur(20px); -webkit-backdrop-filter: blur(20px); text-align: center; }
  .logo { width: 46px; height: 46px; margin: 0 auto 14px; display: flex; align-items: center; justify-content: center; background: var(--accent); border-radius: 14px; color: #fff8f2; font-weight: 700; font-size: 18px; box-shadow: 0 6px 18px color-mix(in srgb, var(--accent) 42%, transparent); }
  h1 { font-size: 18px; margin: 0 0 6px; letter-spacing: 0.2px; }
  p { color: var(--muted); font-size: 14px; line-height: 1.7; margin: 8px 0; }
  .node { margin: 0 0 20px; word-break: break-all; }
  input { width: 100%; padding: 12px 14px; border: 1px solid var(--border-strong); border-radius: 12px; font-size: 15px; background: var(--surface-input); color: var(--fg); outline: none; font: inherit; }
  input:focus { border-color: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 22%, transparent); }
  .btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; border: 0; background: var(--accent); color: #fff8f2; font-size: 15px; padding: 12px 26px; border-radius: 12px; cursor: pointer; font-weight: 600; text-decoration: none; transition: background 0.15s ease, transform 0.12s ease; }
  .btn:hover { background: var(--accent-strong); }
  .btn:disabled { opacity: 0.55; cursor: default; }
  .btn.block { width: 100%; margin-top: 12px; }
  .ok { color: var(--ok); font-weight: 600; display: inline-flex; align-items: center; gap: 6px; }
  .err { color: var(--danger); }
  .link { display: inline-flex; align-items: center; gap: 8px; margin-top: 16px; color: var(--accent); text-decoration: none; font-weight: 600; }
  .code { font-family: ui-monospace, SFMono-Regular, monospace; background: var(--chip-bg); color: var(--fg); border-radius: 12px; padding: 12px 14px; font-size: 13px; word-break: break-all; margin: 14px 0; }
  .foot { color: var(--muted); font-size: 12px; margin-top: 36px; text-align: center; }
  .foot code { background: var(--chip-bg); padding: 2px 8px; border-radius: 6px; font-size: 11px; }
  .head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
  .brand { display: flex; align-items: center; gap: 12px; }
  .logo.sm { width: 42px; height: 42px; margin: 0; border-radius: 12px; font-size: 16px; }
  .subtitle { color: var(--muted); font-size: 12px; margin: 0; }
  .count { font-size: 12px; font-weight: 600; color: var(--muted); background: var(--chip-bg); padding: 5px 12px; border-radius: 999px; white-space: nowrap; }
  .toolbar { display: flex; align-items: center; gap: 14px; margin-top: 16px; margin-bottom: 4px; }
  .toolbar label { display: flex; align-items: center; gap: 7px; font-size: 13px; color: var(--muted); cursor: pointer; user-select: none; }
  .toolbar input[type=checkbox] { width: auto; accent-color: var(--accent); }
  .card.row { width: 100%; border-radius: 18px; padding: 16px 18px; margin-top: 10px; box-shadow: var(--shadow-card); display: flex; align-items: center; justify-content: space-between; gap: 14px; text-align: left; transition: transform 0.12s ease, box-shadow 0.12s ease; border: 1px solid var(--border); }
  .card.row:hover { transform: translateY(-1px); box-shadow: var(--shadow); }
  .card.row.offline { opacity: 0.72; }
  .card.row.offline:hover { opacity: 1; }
  .card-main { min-width: 0; }
  .name-row { display: flex; align-items: center; gap: 8px; }
  .name { font-weight: 650; font-size: 15px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .badge { font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 999px; flex-shrink: 0; }
  .badge.on { color: var(--ok); background: color-mix(in srgb, #16a34a 14%, transparent); }
  .badge.off { color: var(--muted); background: color-mix(in srgb, currentColor 10%, transparent); }
  .meta { color: var(--muted); font-size: 12px; font-family: ui-monospace, SFMono-Regular, monospace; margin-top: 4px; word-break: break-all; }
  .meta .bound { font-family: system-ui, sans-serif; opacity: 0.85; }
  .lock { color: var(--accent); font-size: 13px; cursor: default; }
  .actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
  .icon-btn { width: 34px; height: 34px; border: 1px solid var(--border); background: transparent; color: var(--muted); border-radius: 10px; cursor: pointer; display: flex; align-items: center; justify-content: center; font-size: 15px; transition: background 0.12s ease, color 0.12s ease; }
  .icon-btn:hover { background: var(--chip-bg); color: var(--fg); }
  .icon-btn.danger:hover { color: var(--danger); background: color-mix(in srgb, #dc2626 10%, transparent); }
  .open { border: 0; background: var(--accent); color: #fff8f2; padding: 9px 18px; border-radius: 11px; cursor: pointer; font-size: 14px; text-decoration: none; font-weight: 600; white-space: nowrap; transition: background 0.15s ease; }
  .open:hover { background: var(--accent-strong); }
  .empty { color: var(--muted); text-align: center; margin-top: 72px; line-height: 1.8; }
  .empty .hint { font-size: 13px; opacity: 0.8; }
  #toast { position: fixed; bottom: 22px; left: 50%; transform: translateX(-50%) translateY(16px); background: var(--fg); color: var(--card-bg); font-size: 13px; padding: 9px 18px; border-radius: 10px; opacity: 0; pointer-events: none; transition: opacity 0.18s ease, transform 0.18s ease; }
  #toast.show { opacity: 1; transform: translateX(-50%); }
</style>`
}

func nodeAuthPageHTML(nodeName, authURL string, hasError bool) string {
	errHTML := ""
	if hasError {
		errHTML = `<p class="err">密码不正确，请重试。</p>`
	}
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Curvature Relay - 节点访问验证</title>
` + pageStyles() + `
</head>
<body>
<div class="center">
  <div class="card">
    <div class="logo">C</div>
    <h1>节点访问验证</h1>
    <p class="node">` + html.EscapeString(nodeName) + `</p>
    <form method="post" action="` + authURL + `">
      <input type="password" name="password" placeholder="请输入访问密码" required autofocus autocomplete="current-password">
      <button class="btn block" type="submit">进入节点</button>
    </form>
    ` + errHTML + `
    <p class="foot" style="margin-top:14px">curvature relay</p>
  </div>
</div>
</body>
</html>`
}

func loginPageHTML(base string) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Curvature Relay</title>
` + pageStyles() + `
</head>
<body>
<div class="center">
  <div class="card">
    <div class="logo">C</div>
    <h1>Curvature Relay</h1>
    <p>此实例为自建 relay，无需登录。若页面跳转到这里，请返回节点列表重新进入。</p>
    <a class="btn" href="/nodes">返回节点列表</a>
  </div>
</div>
</body>
</html>`
}

func bindPageHTML(code, root string) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Curvature Relay - 绑定设备</title>
` + pageStyles() + `
</head>
<body>
<div class="center">
  <div class="card" id="card">
    <div class="logo">C</div>
    <h1 style="font-size:19px">绑定 Curvature 设备</h1>
    <p id="status">正在查询绑定状态...</p>
    <div class="code" id="code" style="display:none"></div>
    <button class="btn block" id="confirmBtn" style="display:none">确认绑定</button>
    <a class="link" id="openLink" style="display:none">打开节点</a>
  </div>
</div>
<script>
const params = new URLSearchParams(location.search);
const bindCode = params.get("code") || "";
const root = params.get("root") || "";
const card = document.getElementById("card");
const statusEl = document.getElementById("status");
const codeEl = document.getElementById("code");
const confirmBtn = document.getElementById("confirmBtn");
const openLink = document.getElementById("openLink");
let pollTimer = null;

function setConfirmBtn(visible) {
  confirmBtn.style.display = visible ? "inline-block" : "none";
}

function renderState(state) {
  const st = state.status;
  if (st === "confirmed") {
    statusEl.textContent = "绑定成功";
    statusEl.className = "ok";
    setConfirmBtn(false);
    codeEl.style.display = "none";
    openLink.style.display = "inline-flex";
    let url = state.node_url;
    if (root) url += "?root=" + encodeURIComponent(root);
    openLink.href = url;
    openLink.textContent = "打开节点";
    clearInterval(pollTimer);
  } else if (st === "pending") {
    statusEl.textContent = state.node_name ? "确认绑定设备 " + state.node_name + "？" : "确认绑定这台设备？";
    codeEl.style.display = "block";
    codeEl.textContent = bindCode;
    setConfirmBtn(true);
  } else if (st === "expired") {
    statusEl.textContent = "绑定码已过期，请回到本地 Curvature 页面重新绑定";
    statusEl.className = "err";
    setConfirmBtn(false);
  } else {
    statusEl.textContent = "绑定状态：" + st;
    statusEl.className = "err";
    setConfirmBtn(false);
  }
}

async function load() {
  try {
    const res = await fetch("/api/bind/status?code=" + encodeURIComponent(bindCode));
    const state = await res.json();
    renderState(state);
  } catch (e) {
    statusEl.textContent = "查询失败";
    statusEl.className = "err";
  }
}

confirmBtn.addEventListener("click", async () => {
  if (confirmBtn.textContent === "确认中...") return;
  confirmBtn.disabled = true;
  confirmBtn.textContent = "确认中...";
  try {
    const res = await fetch("/api/bind/confirm?code=" + encodeURIComponent(bindCode), { method: "POST" });
    const state = await res.json();
    renderState(state);
    if (state.status !== "confirmed") {
      statusEl.textContent = state.error || "绑定失败";
      statusEl.className = "err";
      confirmBtn.disabled = false;
      confirmBtn.textContent = "确认绑定";
    }
  } catch (e) {
    statusEl.textContent = "确认失败";
    statusEl.className = "err";
    confirmBtn.disabled = false;
    confirmBtn.textContent = "确认绑定";
  }
});

pollTimer = setInterval(load, 3000);
load();
</script>
</body>
</html>`
}

func nodesPageHTML(base string) string {
	baseHTML := html.EscapeString(base)
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Curvature Relay - 节点列表</title>
` + pageStyles() + `
</head>
<body>
<div class="wrap">
  <div class="head">
    <div class="brand">
      <div class="logo sm">C</div>
      <div>
        <h1>Curvature 节点</h1>
        <p class="subtitle">已连接到这台 relay 的设备</p>
      </div>
    </div>
    <span class="count" id="count">0 个节点</span>
  </div>
  <div class="toolbar" id="toolbar" style="display:none">
    <label><input type="checkbox" id="showOffline"> 显示离线节点</label>
  </div>
  <div id="list"><p class="empty">加载中...</p></div>
  <div class="foot">Relay 地址：<code>` + baseHTML + `</code></div>
</div>
<div id="toast"></div>
<script>
let allItems = [];

async function load() {
  const listEl = document.getElementById("list");
  try {
    const res = await fetch("/api/devices");
    allItems = await res.json();
    render();
  } catch (e) {
    listEl.innerHTML = '<p class="empty">加载失败</p>';
  }
}

function render() {
  const listEl = document.getElementById("list");
  const countEl = document.getElementById("count");
  const toolbarEl = document.getElementById("toolbar");
  const showOffline = document.getElementById("showOffline").checked;
  const items = allItems.filter(d => showOffline || d.online);
  countEl.textContent = allItems.length + " 个节点" + (allItems.length !== items.length ? "（显示 " + items.length + " 个在线）" : "");
  toolbarEl.style.display = allItems.some(d => !d.online) ? "flex" : "none";
  if (!items.length) {
    listEl.innerHTML = allItems.length
      ? '<p class="empty">所有节点均离线<br><span class="hint">勾选「显示离线节点」查看</span></p>'
      : '<p class="empty">还没有绑定的节点<br><span class="hint">在本地 Curvature 页面点击「从公网访问」即可绑定这台设备</span></p>';
    return;
  }
  listEl.innerHTML = items.map((d) => {
    const badge = d.online
      ? '<span class="badge on">在线</span>'
      : '<span class="badge off">离线</span>';
    const bound = d.bound_at ? new Date(d.bound_at) : null;
    const boundText = bound && !isNaN(bound)
      ? ' · 绑定于 ' + bound.toLocaleString()
      : '';
    const pwIcon = d.has_password ? '<span class="lock" title="已设置访问密码">🔒</span>' : '';
    const offClass = d.online ? '' : ' offline';
    return '<div class="card row' + offClass + '">' +
      '<div class="card-main">' +
        '<div class="name-row"><div class="name">' + escapeHtml(d.node_name) + '</div>' + badge + pwIcon + '</div>' +
        '<div class="meta">' + escapeHtml(d.node_id) + '<span class="bound">' + escapeHtml(boundText) + '</span></div>' +
      '</div>' +
      '<div class="actions">' +
        '<button class="icon-btn" title="重命名节点" onclick="renameNode(this)">✎</button>' +
        '<button class="icon-btn danger" title="删除节点" onclick="deleteNode(this)">✕</button>' +
        '<a class="open" href="' + escapeHtml(d.node_url) + '">打开</a>' +
      '</div>' +
    '</div>';
  }).join("");
}

document.getElementById("showOffline").addEventListener("change", render);

async function deleteNode(btn) {
  const card = btn.closest(".card");
  const name = card.querySelector(".name").textContent;
  if (!window.confirm("确认解绑节点「" + name + "」？\n\n该节点将从 relay 移除，之后需要重新绑定。")) return;
  const nodeId = card.querySelector(".meta").textContent.split('·')[0].trim();
  btn.disabled = true;
  btn.textContent = "…";
  try {
    const res = await fetch("/api/devices/" + encodeURIComponent(nodeId), { method: "DELETE" });
    const result = await res.json();
    if (result.deleted) {
      toast("已解绑「" + name + "」");
      allItems = allItems.filter(d => d.node_id !== nodeId);
      render();
    } else {
      toast(result.error || "删除失败");
      btn.disabled = false;
      btn.textContent = "✕";
    }
  } catch (e) {
    toast("请求失败");
    btn.disabled = false;
    btn.textContent = "✕";
  }
}

async function renameNode(btn) {
  const card = btn.closest(".card");
  const nameEl = card.querySelector(".name");
  const currentName = nameEl.textContent;
  const newNodeName = window.prompt("输入新的节点名称：", currentName);
  if (newNodeName === null) return;
  const trimmed = String(newNodeName || "").trim();
  if (!trimmed) {
    toast("节点名称不能为空");
    return;
  }
  const nodeId = card.querySelector(".meta").textContent.split('·')[0].trim();
  btn.disabled = true;
  try {
    const res = await fetch("/api/devices/" + encodeURIComponent(nodeId) + "/name", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ node_name: trimmed }),
    });
    const result = await res.json();
    if (result.node_name) {
      const idx = allItems.findIndex(d => d.node_id === nodeId);
      if (idx >= 0) allItems[idx].node_name = result.node_name;
      toast("已重命名为「" + result.node_name + "」");
      render();
    } else {
      toast(renameErrorText(result.error) || "重命名失败");
      btn.disabled = false;
    }
  } catch (e) {
    toast("请求失败");
    btn.disabled = false;
  }
}

function renameErrorText(code) {
  if (code === "node_name_taken") return "该名称已被其他节点使用";
  if (code === "node_name_required" || code === "node_name_invalid") return "节点名称无效";
  return "";
}

function toast(text) {
  const el = document.getElementById("toast");
  el.textContent = text;
  el.classList.add("show");
  clearTimeout(el._t);
  el._t = setTimeout(() => el.classList.remove("show"), 2200);
}

function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s || "";
  return d.innerHTML;
}
document.getElementById("showOffline").addEventListener("change", render);
load();
setInterval(load, 5000);
</script>
</body>
</html>`
}

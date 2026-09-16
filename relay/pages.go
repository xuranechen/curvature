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
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, -apple-system, sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; background: radial-gradient(1200px 600px at 50% -10%, color-mix(in srgb, #2563eb 12%, transparent), transparent), #f5f6f8; padding: 16px; }
  .card { background: var(--card-bg, #fff); border-radius: 16px; padding: 36px; width: 400px; max-width: 100%; box-shadow: 0 12px 40px rgba(0,0,0,.10); text-align: center; }
  .logo { width: 44px; height: 44px; margin: 0 auto 16px; display: flex; align-items: center; justify-content: center; background: #2563eb; border-radius: 12px; color: #fff; font-weight: 700; font-size: 18px; }
  h1 { font-size: 18px; margin: 0 0 6px; }
  .node { color: var(--muted, #888); font-size: 13px; margin: 0 0 20px; word-break: break-all; }
  input { width: 100%; padding: 11px 14px; border: 1px solid var(--border, #e2e5ea); border-radius: 10px; font-size: 15px; background: var(--input-bg, #fff); color: inherit; outline: none; }
  input:focus { border-color: #2563eb; box-shadow: 0 0 0 3px color-mix(in srgb, #2563eb 18%, transparent); }
  .btn { width: 100%; border: 0; background: #2563eb; color: #fff; font-size: 15px; padding: 12px; border-radius: 10px; cursor: pointer; margin-top: 12px; font-weight: 600; }
  .btn:hover { background: #1d4ed8; }
  .err { color: #dc2626; font-size: 13px; margin: 12px 0 0; }
  .foot { color: var(--muted, #888); font-size: 12px; margin-top: 14px; }
</style>
</head>
<body>
<div class="card">
  <div class="logo">C</div>
  <h1>节点访问验证</h1>
  <p class="node">` + html.EscapeString(nodeName) + `</p>
  <form method="post" action="` + authURL + `">
    <input type="password" name="password" placeholder="请输入访问密码" required autofocus autocomplete="current-password">
    <button class="btn" type="submit">进入节点</button>
  </form>
  ` + errHTML + `
  <p class="foot">curvature relay</p>
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
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, -apple-system, sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; background: radial-gradient(1200px 600px at 50% -10%, color-mix(in srgb, #2563eb 12%, transparent), transparent), #f5f6f8; }
  .card { background: var(--card-bg, #fff); border-radius: 16px; padding: 36px; width: 380px; max-width: 92vw; box-shadow: 0 12px 40px rgba(0,0,0,.10); text-align: center; }
  .logo { width: 44px; height: 44px; margin: 0 auto 16px; display: flex; align-items: center; justify-content: center; background: #2563eb; border-radius: 12px; color: #fff; font-weight: 700; font-size: 18px; }
  h1 { font-size: 19px; margin: 0 0 10px; }
  p { color: #666; font-size: 14px; line-height: 1.7; margin: 8px 0 20px; }
  .btn { display: inline-flex; align-items: center; gap: 8px; background: #2563eb; color: #fff; font-size: 15px; padding: 11px 28px; border-radius: 10px; cursor: pointer; text-decoration: none; border: 0; font-weight: 600; }
</style>
</head>
<body>
<div class="card">
  <div class="logo">C</div>
  <h1>Curvature Relay</h1>
  <p>此实例为自建 relay，无需登录。若页面跳转到这里，请返回节点列表重新进入。</p>
  <a class="btn" href="/nodes">返回节点列表</a>
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
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, -apple-system, sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; background: radial-gradient(1200px 600px at 50% -10%, color-mix(in srgb, #2563eb 12%, transparent), transparent), #f5f6f8; padding: 16px; }
  .card { background: var(--card-bg, #fff); border-radius: 16px; padding: 36px; width: 400px; max-width: 100%; box-shadow: 0 12px 40px rgba(0,0,0,.10); text-align: center; }
  .logo { width: 44px; height: 44px; margin: 0 auto 16px; display: flex; align-items: center; justify-content: center; background: #2563eb; border-radius: 12px; color: #fff; font-weight: 700; font-size: 18px; }
  h1 { font-size: 19px; margin: 0 0 8px; }
  p { color: #666; font-size: 14px; line-height: 1.7; margin: 8px 0; }
  .code { font-family: ui-monospace, SFMono-Regular, monospace; background: var(--code-bg, #f3f4f6); color: var(--code-fg, #111); border-radius: 10px; padding: 12px 14px; font-size: 13px; word-break: break-all; margin: 14px 0; }
  .btn { display: inline-flex; align-items: center; justify-content: center; gap: 8px; border: 0; background: #2563eb; color: #fff; font-size: 15px; padding: 11px 26px; border-radius: 10px; cursor: pointer; margin-top: 10px; font-weight: 600; }
  .btn:disabled { opacity: .55; cursor: default; }
  .ok { color: #16a34a; font-weight: 600; display: inline-flex; align-items: center; gap: 6px; }
  .link { display: inline-flex; align-items: center; gap: 8px; margin-top: 16px; color: #2563eb; text-decoration: none; font-weight: 600; }
  .err { color: #dc2626; }
</style>
</head>
<body>
<div class="card" id="card">
  <div class="logo">C</div>
  <h1>绑定 Curvature 设备</h1>
  <p id="status">正在查询绑定状态...</p>
  <div class="code" id="code" style="display:none"></div>
  <button class="btn" id="confirmBtn" style="display:none">确认绑定</button>
  <a class="link" id="openLink" style="display:none">打开节点</a>
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

function renderState(state) {
  const st = state.status;
  if (st === "confirmed") {
    statusEl.textContent = "绑定成功";
    statusEl.className = "ok";
    openLink.style.display = "inline-flex";
    let url = state.node_url;
    if (root) url += "?root=" + encodeURIComponent(root);
    openLink.href = url;
    openLink.textContent = "打开节点";
  } else if (st === "pending") {
    statusEl.textContent = state.node_name ? "确认绑定设备 " + state.node_name + "？" : "确认绑定这台设备？";
    codeEl.style.display = "block";
    codeEl.textContent = bindCode;
    confirmBtn.style.display = "inline-block";
  } else if (st === "expired") {
    statusEl.textContent = "绑定码已过期，请回到本地 Curvature 页面重新绑定";
    statusEl.className = "err";
  } else {
    statusEl.textContent = "绑定状态：" + st;
    statusEl.className = "err";
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
  confirmBtn.disabled = true;
  confirmBtn.textContent = "确认中...";
  try {
    const res = await fetch("/api/bind/confirm?code=" + encodeURIComponent(bindCode), { method: "POST" });
    const state = await res.json();
    renderState(state);
    if (state.status !== "confirmed") {
      statusEl.textContent = state.error || "绑定失败";
      statusEl.className = "err";
    }
  } catch (e) {
    statusEl.textContent = "确认失败";
    statusEl.className = "err";
  }
});

load();
setInterval(load, 3000);
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
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, -apple-system, sans-serif; margin: 0; background: radial-gradient(1200px 500px at 50% -10%, color-mix(in srgb, #2563eb 10%, transparent), transparent), var(--page-bg, #f5f6f8); color: var(--fg, #111); min-height: 100vh; padding: 28px 20px 48px; }
  .wrap { max-width: 720px; margin: 0 auto; }
  .head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 6px; }
  .brand { display: flex; align-items: center; gap: 12px; }
  .logo { width: 38px; height: 38px; display: flex; align-items: center; justify-content: center; background: #2563eb; border-radius: 10px; color: #fff; font-weight: 700; font-size: 16px; flex-shrink: 0; }
  h1 { font-size: 19px; margin: 0; }
  .subtitle { color: var(--muted, #888); font-size: 12px; margin: 0; }
  .count { font-size: 12px; font-weight: 600; color: var(--muted, #888); background: var(--chip-bg, #eef0f3); padding: 5px 12px; border-radius: 999px; white-space: nowrap; }
  .toolbar { display: flex; align-items: center; gap: 14px; margin-top: 16px; margin-bottom: 4px; }
  .toolbar label { display: flex; align-items: center; gap: 7px; font-size: 13px; color: var(--muted, #888); cursor: pointer; user-select: none; }
  .toolbar input[type=checkbox] { accent-color: #2563eb; }
  .card { background: var(--card-bg, #fff); border-radius: 14px; padding: 16px 18px; margin-top: 10px; box-shadow: 0 3px 14px rgba(0,0,0,.05); display: flex; align-items: center; justify-content: space-between; gap: 14px; transition: transform .12s ease, box-shadow .12s ease; }
  .card:hover { transform: translateY(-1px); box-shadow: 0 6px 22px rgba(0,0,0,.09); }
  .card.offline { opacity: .72; }
  .card.offline:hover { opacity: 1; }
  .card-main { min-width: 0; }
  .name-row { display: flex; align-items: center; gap: 8px; }
  .name { font-weight: 650; font-size: 15px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .badge { font-size: 11px; font-weight: 600; padding: 2px 8px; border-radius: 999px; flex-shrink: 0; }
  .badge.on { color: #15803d; background: color-mix(in srgb, #16a34a 14%, transparent); }
  .badge.off { color: var(--muted, #888); background: color-mix(in srgb, currentColor 10%, transparent); }
  .meta { color: var(--muted, #888); font-size: 12px; font-family: ui-monospace, SFMono-Regular, monospace; margin-top: 4px; word-break: break-all; }
  .meta .bound { font-family: system-ui, sans-serif; opacity: .85; }
  .actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
  .icon-btn { width: 34px; height: 34px; border: 1px solid var(--border, #e2e5ea); background: transparent; color: var(--muted, #888); border-radius: 9px; cursor: pointer; display: flex; align-items: center; justify-content: center; font-size: 15px; transition: background .12s ease, color .12s ease; }
  .icon-btn:hover { background: var(--chip-bg, #eef0f3); color: var(--fg, #111); }
  .icon-btn.danger:hover { color: #dc2626; background: color-mix(in srgb, #dc2626 10%, transparent); }
  .open { border: 0; background: #2563eb; color: #fff; padding: 9px 18px; border-radius: 9px; cursor: pointer; font-size: 14px; text-decoration: none; font-weight: 600; white-space: nowrap; }
  .open:hover { background: #1d4ed8; }
  .empty { color: var(--muted, #888); text-align: center; margin-top: 72px; line-height: 1.8; }
  .empty .hint { font-size: 13px; opacity: .8; }
  .foot { color: var(--muted, #888); font-size: 12px; margin-top: 36px; text-align: center; }
  .foot code { background: var(--chip-bg, #eef0f3); padding: 2px 8px; border-radius: 6px; font-size: 11px; }
  #toast { position: fixed; bottom: 22px; left: 50%; transform: translateX(-50%) translateY(16px); background: var(--fg, #111); color: var(--card-bg, #fff); font-size: 13px; padding: 9px 18px; border-radius: 10px; opacity: 0; pointer-events: none; transition: opacity .18s ease, transform .18s ease; }
  #toast.show { opacity: 1; transform: translateX(-50%); }
</style>
</head>
<body>
<div class="wrap">
  <div class="head">
    <div class="brand">
      <div class="logo">C</div>
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
  const countEl = document.getElementById("count");
  const toolbarEl = document.getElementById("toolbar");
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
    const pwIcon = d.has_password ? '<span title="已设置访问密码" style="color:#2563eb;font-size:13px;cursor:default">🔒</span>' : '';
    const offClass = d.online ? '' : ' offline';
    return '<div class="card' + offClass + '">' +
      '<div class="card-main">' +
        '<div class="name-row"><div class="name">' + escapeHtml(d.node_name) + '</div>' + badge + pwIcon + '</div>' +
        '<div class="meta">' + escapeHtml(d.node_id) + '<span class="bound">' + escapeHtml(boundText) + '</span></div>' +
      '</div>' +
      '<div class="actions">' +
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

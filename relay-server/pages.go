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
	nodeName := html.EscapeString(r.URL.Query().Get("node_name"))
	writeHTML(w, bindPageHTML(code, root, nodeName))
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
			"node_id":   d.NodeID,
			"node_name": d.NodeName,
			"node_url":  s.binds.nodeURL(d.NodeID),
			"online":    s.hub.get(d.NodeID) != nil,
			"bound_at":  d.BoundAt,
		})
	}
	respondJSON(w, http.StatusOK, items)
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
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
  body { font-family: system-ui, sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; background: #f5f6f8; }
  .card { background: var(--card-bg, #fff); border-radius: 14px; padding: 32px; width: 360px; max-width: 92vw; box-shadow: 0 8px 30px rgba(0,0,0,.08); text-align: center; }
  h1 { font-size: 20px; margin: 0 0 10px; }
  p { color: #666; font-size: 14px; line-height: 1.6; margin: 8px 0 18px; }
  .btn { display: inline-block; border: 0; background: #2563eb; color: #fff; font-size: 15px; padding: 10px 26px; border-radius: 8px; cursor: pointer; text-decoration: none; }
</style>
</head>
<body>
<div class="card">
  <h1>Curvature Relay</h1>
  <p>此实例为自建 relay，无需登录。若页面跳转到这里，请返回节点列表重新进入。</p>
  <a class="btn" href="/nodes">返回节点列表</a>
</div>
</body>
</html>`
}

func bindPageHTML(code, root, nodeName string) string {
	return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Curvature Relay - 绑定设备</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; margin: 0; display: flex; min-height: 100vh; align-items: center; justify-content: center; background: #f5f6f8; }
  .card { background: var(--card-bg, #fff); border-radius: 14px; padding: 32px; width: 380px; max-width: 92vw; box-shadow: 0 8px 30px rgba(0,0,0,.08); text-align: center; }
  h1 { font-size: 20px; margin: 0 0 8px; }
  p { color: #666; font-size: 14px; line-height: 1.6; margin: 8px 0; }
  .code { font-family: ui-monospace, monospace; background: #f0f0f3; border-radius: 8px; padding: 10px; font-size: 13px; word-break: break-all; margin: 12px 0; }
  .btn { display: inline-block; border: 0; background: #2563eb; color: #fff; font-size: 15px; padding: 10px 24px; border-radius: 8px; cursor: pointer; margin-top: 8px; }
  .btn:disabled { opacity: .5; cursor: default; }
  .ok { color: #16a34a; font-weight: 600; }
  .link { display: inline-block; margin-top: 14px; color: #2563eb; text-decoration: none; font-weight: 600; }
  .err { color: #dc2626; }
</style>
</head>
<body>
<div class="card" id="card">
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
const nodeName = params.get("node_name") || "";
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
    openLink.style.display = "inline-block";
    let url = state.node_url;
    if (root) url += "?root=" + encodeURIComponent(root);
    openLink.href = url;
    openLink.textContent = "打开节点";
  } else if (st === "pending") {
    statusEl.textContent = nodeName ? "确认绑定设备 " + nodeName + "？" : "确认绑定这台设备？";
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
  body { font-family: system-ui, sans-serif; margin: 0; background: #f5f6f8; padding: 24px; }
  .wrap { max-width: 720px; margin: 0 auto; }
  h1 { font-size: 20px; }
  .card { background: var(--card-bg, #fff); border-radius: 12px; padding: 18px 20px; margin-top: 14px; box-shadow: 0 4px 16px rgba(0,0,0,.06); display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .name { font-weight: 600; font-size: 15px; }
  .meta { color: #888; font-size: 12px; font-family: ui-monospace, monospace; word-break: break-all; margin-top: 4px; }
  .dot { width: 9px; height: 9px; border-radius: 50%; display: inline-block; margin-right: 6px; }
  .on { background: #16a34a; }
  .off { background: #9ca3af; }
  .open { border: 0; background: #2563eb; color: #fff; padding: 8px 18px; border-radius: 8px; cursor: pointer; font-size: 14px; text-decoration: none; }
  .empty { color: #888; text-align: center; margin-top: 60px; }
  .foot { color: #aaa; font-size: 12px; margin-top: 30px; text-align: center; }
</style>
</head>
<body>
<div class="wrap">
  <h1>Curvature 节点</h1>
  <div id="list"><p class="empty">加载中...</p></div>
  <div class="foot">Relay 地址：` + baseHTML + `</div>
</div>
<script>
async function load() {
  const el = document.getElementById("list");
  try {
    const res = await fetch("/api/devices");
    const items = await res.json();
    if (!items.length) {
      el.innerHTML = '<p class="empty">还没有绑定的节点，请在本地 Curvature 页面点击「从公网访问」。</p>';
      return;
    }
    el.innerHTML = items.map((d) => {
      const dot = d.online ? '<span class="dot on"></span>' : '<span class="dot off"></span>';
      return '<div class="card"><div><div class="name">' + dot + escapeHtml(d.node_name) + '</div>' +
        '<div class="meta">' + escapeHtml(d.node_id) + '</div></div>' +
        '<a class="open" href="' + escapeHtml(d.node_url) + '">打开</a></div>';
    }).join("");
  } catch (e) {
    el.innerHTML = '<p class="empty">加载失败</p>';
  }
}
function escapeHtml(s) {
  const d = document.createElement("div");
  d.textContent = s || "";
  return d.innerHTML;
}
load();
setInterval(load, 5000);
</script>
</body>
</html>`
}

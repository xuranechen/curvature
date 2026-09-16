# Curvature Relay（自建中转）

一个兼容 **Curvature relay 协议**的自托管中转服务。家里运行 Curvature 的设备主动连上你公网服务器上的这个 relay，你在任何地方通过自己域名的公网地址访问家里的 Curvature——**不经过任何第三方，不装任何内网穿透工具**。

```
手机/笔记本 ── https://relay.你的域名.com/n/{node_id}/ ──▶ [relay]
                                                            │  WebSocket + yamux
                                                        [家里 Curvature]
```

## 协议实现

参考 Curvature 客户端协议（`backend/internal/relay/*`），实现了：

- `GET /api/bind/poll` 绑定轮询（设备侧）
- `POST /api/bind/confirm`、`GET /api/bind/status` 绑定确认（网页）
- `GET /bind` 确认绑定网页（设备绑定后本地页面会自动打开）
- `GET /ws` 设备 WebSocket 接入，其上运行 yamux 多路复用
- `GET /n/{node_id}/...` 公网访问入口（HTTP + WebSocket 全支持）
- `PUT/DELETE /api/device/nodes/{node_id}/services/{slug}` 本地服务暴露
- 子域名入口 `{slug}-{node_id}-relay.你的域名` 访问暴露的本地服务
- `PUT/DELETE /api/device/access-password` 节点访问密码（设备侧设置）
- `PUT /api/device/node-name` 节点重命名（设备侧设置，同步到节点列表/验证页）
- `GET /api/auth/me`、`GET /login` 前端兼容端点（前端在 relay 模式下 WS 断开会探测 `/api/auth/me`，返回 200 避免误判未登录跳转）
- `GET /nodes` 设备列表页

## 编译

在你本地（有 Go 1.22+ 即可），交叉编译出 Linux 版单文件：

```bash
# Linux x86-64
GOOS=linux GOARCH=amd64 go build -o curvature-relay ./relay
# 或本机直接编译（Windows 自用）
go build -o curvature-relay.exe ./relay
```

产物只有一个文件，上传到服务器即可运行，服务器上**不需要装任何 runtime、不需要下载依赖**。

## 部署（Docker）

仓库根目录已提供 `Dockerfile` 和 `docker-compose.yml`。需要 Docker 的服务器上：

```bash
BASE_URL=https://relay.你的域名.com docker compose up -d --build
```

- `BASE_URL` 必填，是公网访问地址（用于生成设备 WebSocket 端点和节点 URL）
- 数据目录 `/data` 挂载为 `relay-data` 卷（绑定记录、设备、服务注册持久化）

## 部署（原生二进制）

### 1. 服务器运行 relay

```bash
./curvature-relay -addr :8080 -base https://relay.你的域名.com -data ./data
```

- `-base` 必填，是公网访问地址
- `-data` 数据目录（绑定记录、设备、服务注册持久化在这里）

### 2. 域名 + HTTPS + 泛域名

用 Caddy 最简单（自动申请证书）：

```
# Caddyfile
relay.你的域名.com {
    reverse_proxy 127.0.0.1:8080
}

*.relay.你的域名.com {
    reverse_proxy 127.0.0.1:8080
}
```

DNS 需要两条解析（都指向服务器 IP）：
- `relay.你的域名.com` — 绑定确认页、节点入口
- `*.relay.你的域名.com` — **本地服务暴露功能**需要（`{slug}-{node_id}-relay.你的域名`），只用 Curvature 远程访问可不配

云服务器安全组放行 80/443 即可，relay 本身监听 8080 只对本机 Caddy 开放。

### 3. 家里客户端指向你的 relay

启动 Curvature 前设置环境变量：

```bash
export CURVATURE_RELAY_BASE_URL="https://relay.你的域名.com"
curvature -addr 0.0.0.0:7331
```

也可以在配置文件中指定 `relayBaseURL`（参考项目根目录 `config.json` 模板）。

### 4. 绑定

1. 本地打开 Curvature 页面，左下角点「从公网访问」
2. 页面跳到 `https://relay.你的域名.com/bind?code=...`
3. 点「确认绑定」
4. 确认后跳转回 `https://relay.你的域名.com/n/{node_id}/`，就是家里的 Curvature 了

之后手机/笔记本直接访问该地址即可，设备重启后自动重连（客户端内置指数退避重连）。

## 安全说明

- 设备连接 `/ws` 使用 Bearer token 鉴权（绑定时生成，随机 32 字节）；若该节点设置了访问密码，设备 WebSocket 握手时还需携带同一密码，防设备 token 泄露后直接接管节点
- 节点 URL `/n/{node_id}/` 是随机的不可猜测能力地址（node_id 为 32 位十六进制）；设置访问密码后，访问节点前需先输入密码
- 暴露的本地服务通过 `{slug}-{node_id}-relay.你的域名` 访问，同样依赖随机 node_id
- 如需更强保护，配合 Curvature 的 `-e2ee`（端到端加密 + 配对码），未配对前端无法读取节点内容
- 建议在 Caddy 层加访问日志和限流

## 已知限制

- 未实现登录/多用户：绑定确认页任何知道 code 的人都能确认（code 为 24 字节随机，不可猜测）。前端 WS 断开时的 `/api/auth/me` 探测由 relay 固定返回 200
- WebPush 通知不经过 relay，仍需 Curvature 本地直连配置
- PWA 以 standalone 模式打开 relay 的 `/nodes` 且无 `lastNodeId` 缓存时，前端 bootstrap 会请求 relay 上不存在的 Curvature API（属边缘场景，正常入口是从 `/n/{node_id}/` 打开）

## 测试

`cmd/device-sim` 是一个模拟 Curvature 设备连接器的测试程序，可离线验证 relay 全链路（绑定、HTTP 转发、WebSocket 转发）：

```bash
go run ./cmd/device-sim -base http://127.0.0.1:8080
# 另一终端：go run ./cmd/wscheck -node <node_id> -base http://127.0.0.1:8080
```

## 许可

AGPL-3.0。`wsframes.go` 中 WebSocket 桥接帧格式参考了 Curvature（AGPL-3.0）的 `backend/internal/relay/wsconn.go`。
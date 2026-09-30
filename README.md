# P2P Search

自托管 eD2K / KAD 搜索容器（MVP）。基于 [goed2k/core](https://github.com/goed2k/core)。

## 一键启动

```bash
docker compose up -d --build
```

打开 http://localhost:8080

## API

| 接口 | 说明 |
|------|------|
| `GET /health` | 存活检查 |
| `GET /api/status` | 引擎 / KAD 状态 |
| `GET /api/search?q=关键词&limit=50&wait=15` | 阻塞搜索 |
| `GET /search?q=...` | 同上 |

```bash
curl -s "http://localhost:8080/api/search?q=ubuntu&limit=50&wait=15"
```

返回字段：`filename`、`size`、`ed2k`、`sources`、`complete_sources`、`hash`、`source`。

## 配置

复制 `config.example.yaml` 为 `config.yaml` 后按需修改：

- 优先服务器列表（默认 2026-09 高活跃节点）
- `server.met` 自动地址：`http://upd.emule-security.org/server.met`
- 每 `refresh_hours`（默认 8）小时刷新列表并重连
- KAD / UPnP / 搜索超时与 limit
- 115 离线：在本地 `config.yaml` 填写 cookie（勿提交仓库）

环境变量可覆盖（`GOED2K_SERVERS`、`GOED2K_KAD`、`HTTP_ADDR` 等）。

## 本地运行（Go 1.26+）

```bash
go mod tidy
go run ./cmd/p2psearch
```

## 代理

在 `config.yaml`：

```yaml
proxy:
  url: "http://127.0.0.1:7890"   # 或 socks5://127.0.0.1:7890
```

也可设环境变量：`P2PSEARCH_PROXY` / `PROXY_URL` / `HTTP_PROXY`。

**能搜更多吗？**

| 代理类型 | 作用 |
|----------|------|
| HTTP/HTTPS | 拉取 `server.met` / `nodes.dat`（被墙时更稳）→ 引导更好，间接可能多一点 |
| SOCKS5 / TUN / VPN | 才能代理 **eD2K TCP（搜服务器）**；KAD 还依赖 UDP，系统 TUN 最完整 |

纯 HTTP 代理**不会**把搜索请求本身送进代理。运营商若屏蔽 eD2K 端口，建议 Clash 等开 **TUN 模式**，比只填 HTTP 代理有效得多。

## MVP 范围

已完成：自动连服务器 + KAD、搜索 API、结果含 ed2k、Web 一键复制、Docker、server.met 定时刷新、出站代理配置。

第二阶段预留：115 离线推送、过滤、历史收藏。

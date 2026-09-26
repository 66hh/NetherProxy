# NetherProxy 接口文档

## 信令端点（公开，Minecraft 客户端硬编码路径）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/join` | 服务器名片 (MOTD)，透传匹配的 BDS |
| POST | `/v1/join/{networkID}` | SDP offer/answer 交换；answer 经拦截重写后返回 |

- 按请求 `Host` 头匹配 `bds[]` 的 `domain`（支持 `*` 通配），无匹配返回 404
- POST 处理链：JWT 身份验证（`verify_identity`）→ 限流（`rate_limit`）→ 黑白名单/webhook（`access`）→ 透传 BDS → answer 重写 → 创建数据面会话
- 拒绝时返回与 BDS 一致的数字错误码：`200 + application/sdp` + body `"37"`（IdentityNotAllowed）

## 管理 API（`/api` 前缀）

默认全部需要 `Authorization: Bearer <gateway.token>` 认证。
`gateway.api_auth_exempt` 列表中的路由模板免认证（默认仅 `/api/healthz`）。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/healthz` | 健康检查，返回 `{"status":"ok"}`（默认免认证） |
| GET | `/api/metrics` | Prometheus 指标（需 `metrics.enable: true`） |
| GET | `/api/config` | 读取当前生效配置（`gateway.token` 脱敏为 `"***"`） |
| PUT | `/api/config` | 写入配置（JSON，全量）；校验失败返回 400 + `details`；回传 `"***"` 表示不修改 token；响应含 `restart_required` |
| POST | `/api/config/reload` | 从 config.yml 重新加载；校验失败返回 400 + `details` |
| GET | `/api/entry` | 线路状态：健康、活跃会话数、心跳统计 |
| GET | `/api/session` | 会话列表：玩家名/XUID、客户端地址、状态、双向流量、时长 |
| DELETE | `/api/session/{ufrag}` | 掐断指定会话（玩家立即掉线） |
| GET | `/api/bds` | BDS 状态：健康、心跳统计、状态历史 |
| GET | `/api/log` | 内存日志缓冲。参数：`tail`（默认 200，上限 1000）、`level`（默认 debug） |
| GET | `/api/stats` | 代理状态采样序列（2s 间隔，保留 10 分钟），面板折线图数据源 |

### 错误响应格式

```json
{"error": "config rejected", "details": "validate config: gateway.port: invalid port 99999, expect 1-65535"}
```

## Webhook 契约（`gateway.access.webhook`）

join 时代理向配置的外部服务发起判定请求：

**请求** `POST <url>`，`Content-Type: application/json`：

```json
{"xuid": "2535...", "xname": "PlayerName", "client_ip": "1.2.3.4"}
```

**响应**：HTTP 200 + JSON：

```json
{"allow": true, "reason": "可选的拒绝原因(记入日志)"}
```

- `allow: false`、非 200 状态码、超时、网络错误一律视为**拒绝**（fail-closed）
- 判定在身份验证通过之后执行，xuid/xname 可信

## 前端面板

`GET /` 返回内嵌控制面板（无需认证加载页面，数据请求仍需 token）。
启动时控制台打印面板地址与 token。

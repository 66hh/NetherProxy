# NetherProxy 配置文档

配置文件为 `config.yml`（YAML），首次启动自动生成。支持 `PUT /api/config` 热写与 `POST /api/config/reload` 重载。

标注「热更」的项立即生效；其余修改后 `restart_required` 会提示需要重启。

## log — 日志

| 字段 | 默认 | 说明 |
|---|---|---|
| `level` | `info` | 日志级别：debug/info/warn/error（热更） |
| `format` | `text` | 输出格式：text/json |
| `file` | `""` | 日志文件路径，为空仅输出控制台 |
| `max_size` | `100` | 单文件最大 MB，超过后轮转（0 = 100MB） |
| `max_backups` | `7` | 旧文件保留数量，0 不限 |
| `max_age` | `30` | 旧文件保留天数，0 不限 |
| `compress` | `false` | 轮转后是否 gzip 压缩 |

## gateway — HTTP 信令/管理网关

| 字段 | 默认 | 说明 |
|---|---|---|
| `host` / `port` | `0.0.0.0:19130` | 监听地址 |
| `token` | 随机生成 | API 认证凭据（热更）；不允许为空 |
| `verify_identity` | `true` | 是否验证玩家身份 JWT（热更）。关闭时黑白名单/webhook 自动失效（fail-closed），限流回退按客户端 IP |
| `api_auth_exempt` | `["/api/healthz"]` | 免认证的 API 路由列表（按路由模板匹配，热更） |
| `relay_only` | `false` | 中继模式：offer 中客户端真实地址替换为不可达占位，隐藏玩家 IP、强制流量经代理（热更） |
| `tls.enable` / `cert` / `key` | `false` | TLS 证书（PEM），最低 TLS 1.2 |
| `tls.dual` | `false` | 同端口同时支持明文 HTTP 与 HTTPS（安卓客户端仅 HTTP，iOS 仅 HTTPS） |
| `metrics.enable` | `false` | Prometheus 指标（固定 `/api/metrics`，认证跟随豁免列表） |
| `access` | 见下 | 访问控制（热更） |
| `rate_limit` | 见下 | join 限流（热更） |

### gateway.access — XUID 黑白名单 + webhook

```yaml
access:
  mode: off          # off / blacklist / whitelist
  xuids: []          # XUID 名单
  webhook:
    enable: false
    url: ""          # POST {"xuid","xname","client_ip"} → {"allow":bool}
    timeout: 3s
```

- `blacklist`：名单内拒绝；`whitelist`：名单外拒绝（空名单校验报错）
- webhook 失败（超时/非 200/allow:false）一律拒绝（fail-closed）
- 判定在 JWT 验证通过后执行

### gateway.rate_limit — join 频率限制

```yaml
rate_limit:
  enable: false
  interval: 60s      # 窗口长度
  max_joins: 5       # 每窗口每 key 最大 join 数
  max_keys: 1000     # 跟踪的最大 key 数, 超过整体重置
```

已验签玩家按 XUID 限流；未验签（`verify_identity: false`）按客户端 IP。GET /v1/join（MOTD）始终按 IP 限流。

## multiplexer — UDP 数据面

| 字段 | 默认 | 说明 |
|---|---|---|
| `host` / `port` | `0.0.0.0:19131` | UDP 监听地址，全部玩家数据面复用此端口 |

⚠️ 端口必须避开 BDS 数据面端口池（默认 19132–19139），否则收不到包。

## bds — 后端 BDS 列表（热更）

```yaml
bds:
  - enable: true
    domain: "*"          # 匹配 Host 头: "*" 任意 / "*.example.com" 子域 / 精确匹配; 靠前优先
    host: 127.0.0.1
    port: 19132
    heartbeat:           # BDS 健康探测 (HTTP GET /v1/join)
      enable: false
      manual_only: false # true 时仅记录统计, 无响应由人工处理; false 自动下线
      interval: 5s
      timeout: 2s
      retries: 3
```

## entry — 公网线路列表（热更）

```yaml
entry:
  - enable: true
    host: 1.2.3.4        # 公网地址 (客户端实际连接, 需映射到 multiplexer)
    port: 19131
    max_session: 100     # 最大会话数, 0 不限; 多条线路按最少会话数均衡
    heartbeat:           # 线路心跳 (UDP NPING/NPONG, 不转发到 BDS)
      enable: false
      manual_only: false # true 时仅记录统计, 无响应由人工处理; false 自动下线
      interval: 5s
      timeout: 2s
      retries: 3
```

- 均衡策略：最少活跃会话数优先，跳过已满/心跳下线的线路
- 心跳统计持久化到 `entry_stats.json`，经 `GET /api/entry` 与 Prometheus 暴露

## session — 会话超时（热更）

| 字段 | 默认 | 说明 |
|---|---|---|
| `signaled_timeout` | `30s` | 信令完成后等待首个 STUN 的超时 |
| `active_idle_timeout` | `120s` | 活跃会话空闲转 Idle 的时间 |
| `idle_reap_timeout` | `300s` | 会话自最后活动起被回收的时间 |
| `tuple_stale_timeout` | `60s` | 客户端地址软状态有效期（数据面活动会自动续期） |

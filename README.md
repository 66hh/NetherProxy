# NetherProxy

Minecraft 基岩版 NetherNet 协议（WebRTC：ICE + DTLS + SCTP）的单端口复用边缘代理。

公网仅暴露一个 UDP 端口承载全部玩家数据面，内网 BDS 与客户端均零改动，DTLS 与游戏层加密端到端保持——代理只转发，不终止。

## 特性

- **单端口复用**：全部玩家的 STUN/DTLS/SCTP 流量复用一个 UDP 端口，按 ice-ufrag 路由、按 5-tuple 转发
- **多 BDS 域名路由**：按请求 Host 将信令路由到不同后端，支持通配符
- **多线路负载均衡**：多条公网线路按最少会话数自动均衡，支持容量上限
- **线路健康检测**：主动心跳探测，无响应自动下线、恢复自动上线，统计持久化
- **身份验证**：验证玩家微软签发的身份令牌（JWKS 验签），拒绝伪造连接
- **访问控制**：XUID 黑白名单、Webhook 动态判定、按玩家限流
- **中继模式**：隐藏客户端真实地址，强制全部流量经代理
- **控制面板**：内嵌 Web 管理界面，会话管理、线路/BDS 管理、健康状态图、可视化配置编辑
- **可观测性**：Prometheus 指标、结构化日志（轮转落盘）、线路统计持久化

## 架构

```
玩家                NetherProxy                               内网 BDS
 │  HTTP 信令 ──────► 透传（GET/POST /v1/join）────────────────►│
 │◄──── SDP answer ── 拦截重写 candidate/m=/c= ─────────────────│
 │                    身份验证 · 限流 · 名单 · 建会话             │
 │═ STUN(USERNAME=ufrag) ═► 按 ufrag 路由 + 学习客户端地址 ══════►│
 │═ DTLS/SCTP ═══════════► 按已学习 5-tuple 转发 ══════════════►│
```

## 快速开始

```bash
go build -o netherproxy.exe ./cmd/netherproxy
./netherproxy.exe
```

首次启动生成默认配置 `config.yml`，编辑后重启。启动日志会打印控制面板地址与访问令牌：

```
level=INFO msg="web panel ready" url=http://127.0.0.1:19130/ token=...
level=INFO msg="gateway listening" addr=0.0.0.0:19130
level=INFO msg="multiplexer listening" addr=0.0.0.0:19131
```

客户端添加服务器地址填 `网关主机:网关端口`。

## 文档

- [配置说明](docs/config.md)：全部配置字段、默认值与热更行为
- [接口文档](docs/api.md)：管理 API、Webhook 契约与信令端点

## 控制面板

浏览器访问网关地址即可打开，使用 `gateway.token` 登录。功能包括：

- 会话：实时列表（玩家、流量、状态、时长）、掐断连接、一键拉黑
- 线路/BDS：健康状态与 24 小时状态历史图、增删改、心跳配置
- 配置：按类型分组的表单编辑，保存自动校验并提示是否需要重启

## 监控

开启 `gateway.metrics.enable` 后，Prometheus 从 `/api/metrics` 抓取：

- 信令：HTTP 请求、join/MOTD 结果分类计数
- 数据面：丢包原因分布、全局双向流量
- 会话：活跃数、状态分布、每玩家流量
- 线路/BDS：健康状态、心跳成功率与延迟

## 测试

```bash
# 端到端功能测试（自动启停模拟 BDS，通过管理 API 切换配置）
python tests/run_test.py
```

## 部署要点

1. **数据面端口必须避开 BDS 端口池**：BDS 会把 19132–19139 预绑定到每张网卡地址上，操作系统按"更具体的绑定优先"投递 UDP——代理端口落在池内时将一个包都收不到。
2. **线路地址不要使用回环地址**：客户端 ICE 栈会丢弃指向 127.0.0.1 的远端候选，entry 必须配置真实可达的公网或局域网地址。
3. **同网段旁路**：客户端与 BDS 同网段时，BDS 可对 offer 中的客户端地址反向建连绕开代理；需要强制走代理时开启 `gateway.relay_only`。

## 项目结构

```
cmd/netherproxy/        入口
internal/
  conf/                 配置：加载/校验/热重载/原子落盘
  logger/               日志：slog + 轮转
  session/              会话表：状态机 + 双索引 + 流量统计
  server/
    gateway/            HTTP 信令、SDP 重写、身份验证、管理 API、控制面板
    multiplexer/        UDP 数据面、心跳探测、线路统计
tests/                  端到端功能测试（模拟客户端/BDS）
docs/                   配置与接口文档
```

## 许可证

见 [LICENSE](LICENSE)。

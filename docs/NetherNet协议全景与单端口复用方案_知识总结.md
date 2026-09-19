# NetherNet 协议全景与单端口复用方案 · 知识总结

| 项目 | 内容 |
|---|---|
| 版本 | v1.0 |
| 日期 | 2026-09-19 |
| 范围 | NetherNet 协议机制 + HTTP 信令线协议 + 单端口复用边缘代理设计 |
| 最初输入 | 1.20.50 协议逆向文档；后由社区实现与公开资料补全演进部分 |

---

## 1. NetherNet 是什么

Minecraft 基岩版的新一代传输层协议，基于 WebRTC（ICE + DTLS + SCTP），用于替代 RakNet。

**演进时间线**：

| 版本 | 事件 |
|---|---|
| 1.20.50 | NetherNet 上线，覆盖 LAN（UDP 7551 广播信令）与 Xbox Live 好友会话（微软 WSS 信令）；不支持公网直连 |
| 26.50 | Dedicated Server 支持 `transport=nethernet`；新增 `serveridentity` 身份密钥命令 |
| 26.60 | RakNet 彻底移除，NetherNet 成唯一传输；公网服务器纳入覆盖，新增 HTTP 信令 |

**动机**：RakNet 是裸 UDP，严格 NAT 下无法联机；WebRTC 带来成熟的 NAT 穿透（ICE/STUN/TURN）、传输加密（DTLS）、拥塞控制（SCTP）。商业层面，"不支持直连"使线上流量必经 Xbox Live/微软服务（TURN 成本归属、moderation 能力）。

## 2. 总体架构：信令与数据面分离

```
┌─ 信令层（交换 SDP/candidate，三种通道）────────────────────┐
│  ① LAN：UDP 7551 广播 + DiscoveryMessagePacket              │
│  ② Xbox Live：wss://signal.franchise.minecraft-services.net │
│     （MCToken 认证，下发 STUN/TURN 凭据）                    │
│  ③ HTTP：BDS TCP 端口（19132）的两个端点 ★ 公网直连通道 ★   │
└─────────────────────────────────────────────────────────────┘
                    ↓ SDP offer/answer + candidates
┌─ 数据面（每条连接独立）──────────────────────────────────────┐
│  游戏数据包（RakNet 编码格式）                                │
│   → 分段层（>10,000B 拆包，segment count 倒计时）             │
│   → SCTP DataChannel ×2（Reliable 有序 / Unreliable unordered）│
│   → DTLS（自签证书，fingerprint 经 SDP 交换校验）             │
│   → ICE（STUN 检查；客户端 controlling / 服务端 controlled）  │
│   → UDP                                                       │
└──────────────────────────────────────────────────────────────┘
```

游戏包格式与 RakNet 时代兼容：传输层替换、游戏层无感知。

## 3. LAN 发现协议（1.20.50 逆向）

- UDP 7551，客户端向广播地址发 DiscoveryRequest，服务器回 DiscoveryResponse（服务器名/游戏模式/人数等 ServerData）。
- 包结构：`包长(uint16) + 类型(uint16) + 发送者ID(uint64) + 8字节padding + 数据`，外层 AES-ECB 加密 + HMAC-SHA256，密钥 = `SHA-256(0xdeadbeef)`（**硬编码公开常量 = 混淆，不是安全**；ECB 模式还泄漏块结构）。
- ICE 协商复用 DiscoveryMessagePacket 承载 `MESSAGETYPE CONNECTIONID DATA` 三段式消息（CONNECTREQUEST / CONNECTRESPONSE / CANDIDATEADD）。
- 新版变化：取消包加密、CONNECTERROR 消息类型、WSS JSON 信封（Type/Message/To/From）。

## 4. 端口机制的完整解答

### 4.1 为什么"每玩家一个端口"

WebRTC 的连通性建立在 candidate pair（本端 IP:端口 ↔ 对端 IP:端口）上，**一条连接 = 一个本端地址**。客户端连接数 = 服务端端口数。这是 ICE 模型的内生记账方式，不是协议缺陷。

RakNet 能单端口：应用层按源地址解复用。NetherNet 不解复用：DTLS/SCTP 包头无连接标识。主流实现（libwebrtc 血统）默认每 PeerConnection 一个 UDP socket——socket 即路由键，零解复用成本、NAT 映射独立、避免跨连接队头阻塞。协议不强制：pion 的 ICEUDPMux、Janus 等 SFU 早已实现共享端口。

代价：单机端口上限（Linux 临时端口约 2.8 万）+ 多 socket 资源。

### 4.2 端口是谁分配的

| 端口 | 分配者 | 出现在哪 |
|---|---|---|
| 本地 socket 端口 | 操作系统（ephemeral 池），WebRTC 栈发起 `bind(0)` | host candidate |
| NAT 外部端口 | 路由器/NAT 设备 | server-reflexive candidate（经 STUN 反射 XOR-MAPPED-ADDRESS 得知） |
| TURN 中继端口 | TURN 服务器（微软） | relay candidate |
| 7551（LAN 发现） | 游戏写死，直接 bind | discovery |

WebRTC 栈只决定"要几个 socket"，号由内核发。

### 4.3 客户端怎么知道服务端端口

**不是发现，是信令告知**：

1. 服务端 ICE gather 出 candidate（`a=candidate:... <IP> <端口> typ host`），塞进 SDP answer；
2. 客户端解析 SDP 得到端口，发 STUN 检查验证真实可达；
3. DTLS/SCTP/游戏数据复用这一对端口。

SDP 示例里 `m=application 9 ...` 的端口 9 是占位符，真地址全在 candidate 行。

### 4.4 WebRTC 单端口能力 vs 多端口记账

- 单条连接只需一个端口：BUNDLE + rtcp-mux 把全部媒体/数据复用到一个 transport，STUN/DTLS/SCTP 在同一 candidate pair 上 demux。
- 单端口服务万人：RakNet 靠应用层会话复用；WebRTC 需实现者自己做（ufrag demux），即 §7 方案。

## 5. HTTP 信令线协议（公网直连通道）

社区实现（df-mc/go-nethernet 的 `endpoint` 包）完整复刻了原版 BDS 行为。仅两个端点：

### 5.1 GET /v1/join — 服务器名片

```
GET /v1/join HTTP/1.1
User-Agent: libhttpclient/1.0.0.0
Connection: close
```

```
HTTP/1.1 200 OK
Content-Type: application/json

{"name":"Dedicated Server","protocol":2193,"version":"1.26.x",
 "level":"Bedrock level","players":0,"maxPlayers":10,"gameType":0}
```

- `gameType`：0 生存 / 1 创造 / 2 冒险；UA 为原版客户端内置 HTTP 库标识
- 每次请求 `Connection: close`，不 keep-alive
- 等价于 RakNet 时代的 unconnected pong

### 5.2 POST /v1/join/{networkID} — SDP 交换

- `{networkID}` = 客户端自己的随机 uint64（十进制），服务端严格校验，非 uint64 返回 400
- 请求：`Content-Type: application/sdp`，body 为完整非 trickle SDP offer（所有 candidate 内嵌）
- 成功：`200 OK` + `Content-Type: application/sdp` + SDP answer
- **SDP body 上限 1 MiB**（超限 413）
- **单次往返**：HTTP POST 一次完成 offer/answer（与 WHIP 同思路），之后通道废弃
- **trickle ICE 硬性不支持**：服务端无法在 answer 后推 candidate，双方必须先 gather 完

### 5.3 错误处理（数字错误码机制）

answer 失败时 body 是**纯数字错误码**，客户端解析为"协商失败，错误码 N"。HTTP 层错误：

| 状态码 | 含义 |
|---|---|
| 400 | networkID 非 uint64 / body 为空 / 超限 |
| 502 | 等 Listener 产出 answer 超时（默认 15s） |
| 503 | offer 未被接受（服务未就绪/已满） |

### 5.4 客户端发现顺序（NetherNet 优先于 RakNet）

```
https://host:port → https://host (443) → http://host:port → http://host (80)
```

显式端口则只试该端口的 https/http。第一个响应者胜出，全部无响应才回退 RakNet——同地址上 NetherNet 优先级更高。

## 6. 从静态映射到 SDP 重写：公网代理的可行路径

- **静态端口映射的死结**：实测（playit.gg，BDS 1.26.51.1）仅透传信令时要求"UDP 内外网端口号完全一致"——answer 广告的是服务器真实绑定端口，外层翻译后地址对不上。
- **解法是完整重写 answer 中的 candidate**：内外端口彻底解耦。
- 附带事实：TCP 19132 只承载信令；游戏 UDP 流量独立（`server-udp-ports` 池），直连时走 UDP。

## 7. 单端口复用边缘代理设计（核心交付）

### 7.1 目标

公网仅暴露一个 UDP 端口（如 19133）承载全部玩家数据面；内网 BDS per-player 端口分配零改动；不改客户端、不改 BDS；DTLS/游戏加密端到端保持。

### 7.2 三大技术支点

1. **ufrag 即 session key**：SDP answer 中 `a=ice-ufrag` 连接级唯一；客户端之后每个 STUN 包的 USERNAME 属性都携带它（`服务端ufrag:客户端ufrag`）→ 公网端口可按 ufrag 分流。
2. **STUN 可识别**：魔数 `0x2112A442` + USERNAME 属性，可无状态解析。
3. **两级路由**：DTLS/SCTP 无连接标识，但 ICE 提名后客户端 5-tuple 固定——STUN 阶段按 ufrag 路由并学习 5-tuple，数据阶段按 5-tuple 转发。

### 7.3 架构与数据流

```
客户端                边缘代理（公网IP）                          内网 BDS
  │   GET /v1/join ──► TCP 透传 ──────────────────────────────►│
  │   POST offer ────► TCP 透传 ──────────────────────────────►│
  │◄── answer ──────── ★拦截重写 candidate/c-line★ ◄────────────│
  │                     解析 ufrag + 内网端口 Pi                  │
  │                     建 session: ufrag→(BDS_IP,Pi)            │
  │                     建内部 socket: proxyIP:临时端口           │
  │                     （白捡：{networkID} 亦可作信令层关联键）   │
  │                                                                 │
  │═ UDP STUN ═══════► ①解析ufrag查session ②学习5-tuple          │
  │                     ③源地址改写后经内部socket转发 BDS_IP:Pi     │
  │◄══════════════════ BDS回包(源=Pi) → 公网socket发回 ◄══════════│
  │═ DTLS/SCTP ═══════► 非STUN按5-tuple查表，同上                 │
```

服务端视角：每个玩家 = `proxyIP:某临时端口`，完全无感知。

### 7.4 SDP 重写规则

| 字段 | 操作 | 原因 |
|---|---|---|
| `a=candidate:*` | IP→公网IP，端口→19133，**全量**（含 127.0.0.1/内网IP/IPv6/srflx，可只保留一条） | 漏改一条客户端就可能选它 |
| `c=` 连接行 | 地址→公网IP | 部分栈校验 |
| `a=ice-ufrag` | **保持原样** | 路由键 |
| `a=fingerprint` / `a=identity` | **保持原样** | DTLS 端到端与身份绑定 |
| `m=` 行端口 | 不动 | 端口 9 是占位符 |

拦截前先确认 body 真是 SDP：answer 也可能是纯数字错误码（按 Content-Type 或 `v=` 开头判断）。

### 7.5 会话管理

- 状态机：Signaled → ICEChecking（首 STUN）→ Active（DTLS 流量）→ Idle → Closed
- 超时：Signaled 30s 无 STUN 关闭；Active 空闲 120s 转 Idle；Idle 300s 回收；5-tuple 软状态 60s 未刷新删除
- 客户端换网：新 5-tuple 触发重新学习（ufrag 不变，路由不断），旧项超时回收
- 容量：每 session 内存 200–500 B，10 万并发 < 100 MB；瓶颈是 pps 不是内存

### 7.6 实现要点

- 技术栈：Go + `pion/stun` + `pion/sdp`
- 公网侧单 socket 用 recvmmsg/sendmmsg；远期 XDP/eBPF 卸载 STUN 判定
- 多入口容灾：answer 中写多条 candidate 指向多个入口 IP，客户端 ICE 自选
- 意外收益：对称 NAT 玩家只需能出网 UDP 即可连，无需打洞

## 8. 风险清单

| # | 风险 | 严重度 | 对策 |
|---|---|---|---|
| 1 | candidate 漏改 | 高 | 全量处理 + 上线前抓包对照 |
| 2 | BDS 版本在 SDP 外字段（identity JWT claim）嵌端口 | 中 | 版本升级后回归测试 |
| 3 | 代理成数据面单点 | 高 | 多入口 candidate + 双实例 |
| 4 | 代理带宽/延迟成本 | 中 | 同机房部署、按峰值选型 |
| 5 | 5-tuple 学习窗口伪造 | 中 | 非 STUN 包必须匹配已学习 5-tuple，且须经带有效 ufrag 的 STUN 建立 |
| 6 | UDP 单 socket 性能 | 低 | recvmmsg；远期 XDP |
| 7 | 数字错误码误判为 SDP | 低 | 拦截时按 Content-Type 判断 |

## 9. 安全模型

- 代理对 DTLS **透明**（不终止）：游戏内容不可见，游戏层加密（Xbox 身份）不受影响；代理仅见元数据（时间/流量/速率）
- 入口 DDoS：STUN 解析前做无状态速率限制，未知 ufrag 直接丢弃
- 会话劫持：ufrag 连接级随机；可选校验 STUN MESSAGE-INTEGRITY（HMAC 密钥 = SDP 中 ice-pwd，拦截器同样可得）
- LAN 发现加密是混淆级：硬编码密钥，防识别不防伪造，真安全靠上层

## 10. 实施路线

1. **MVP**：单入口、单 BDS、Go userspace 转发；验证基础用例（单玩家进出、并发、断线重连、多网卡、严格 NAT、>10KB 分包）
2. **硬化**：速率限制、HMAC 校验、双入口、指标告警
3. **集群化**：按域名路由到多 BDS 实例，公网每入口仍一个 UDP 端口
4. **卸载**：STUN 判定下沉 XDP/eBPF

## 附录 A：SDP 重写对照示例

原始（answer 片段）：
```
a=ice-ufrag:8F3k
a=candidate:1 1 udp 2122260223 192.168.1.5 40001 typ host
a=candidate:2 1 udp 2122194687 127.0.0.1 40001 typ host
a=candidate:3 1 udp 1686052607 203.0.113.7 40001 typ srflx
c=IN IP4 0.0.0.0
```

重写后：
```
a=ice-ufrag:8F3k                          ← 原样
a=candidate:1 1 udp 2122260223 203.0.113.7 19133 typ host
c=IN IP4 203.0.113.7
（127.0.0.1 与 srflx 两条已删除）
```

session 表：`8F3k → (192.168.1.5:40001)`

## 附录 B：STUN 解析要点

- 判定：长度 ≥ 20 且 `buf[4:8] == 0x2112A442`
- USERNAME：属性 type 0x0006，内容 `"serverUfrag:clientUfrag"`，取**前半**为路由键
- 可选校验：MESSAGE-INTEGRITY（0x0008），HMAC-SHA1，密钥 = SDP `a=ice-pwd`

## 附录 C：参考资料

1. 1.20.50 NetherNet 协议逆向文档（本次分析起点）
2. nethernet-spec（社区规范：WSS JSON 信封、CONNECTERROR、新版变更）
3. df-mc/go-nethernet（HTTP 信令复刻；`endpoint.ServeTLS`；MITM 代理示例）
4. gophertunnel（NetherNet Go 实现）
5. playit.gg 社区实测（2026-09）：TCP 19132 信令 + UDP 分工、内外端口一致性约束
6. BDS 26.50/26.60 更新日志
7. RFC 8445（ICE）、WHIP（draft-ietf-wish-whip，单次 SDP 交换的先例）

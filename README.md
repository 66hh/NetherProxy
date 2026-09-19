# NetherProxy

> ⚠️ **声明：本项目是临时 vibe coding 出来的测试项目**，用于验证
> [NetherNet 单端口复用边缘代理方案](docs/NetherNet协议全景与单端口复用方案_知识总结.md)
> 的可行性。代码未经验收级打磨，仅供学习与研究参考，**请勿用于生产环境**。

Minecraft 基岩版 NetherNet 协议（WebRTC：ICE + DTLS + SCTP）的单端口复用边缘代理：
公网只暴露**一个 UDP 端口**承载全部玩家数据面，内网 BDS 零改动，客户端零改动，
DTLS/游戏加密端到端保持（代理只转发，不终止）。

## 原理（TL;DR）

```
玩家                NetherProxy                        内网 BDS
 │  HTTP 信令 ─────► 透传（GET/POST /v1/join）─────────►│
 │◄──── SDP answer ──★拦截重写 candidate/m=/c= ★────────│
 │                    建会话: 服务端ufrag → BDS内网端口    │
 │═ STUN(USERNAME=ufrag) ═► 按 ufrag 路由+学习客户端地址 ═►│
 │═ DTLS/SCTP ═══════════► 按已学习 5-tuple 转发 ═══════►│
```

- 信令层：HTTP 透传，仅拦截重写 SDP answer（`candidate` 全量替换为代理地址，
  `m=` 端口与 `c=` 行同步；`ice-ufrag`/`fingerprint`/`identity` 原样保留）
- 数据面：单 UDP socket，STUN 按 USERNAME（服务端 ufrag）路由并学习客户端
  5-tuple，后续 DTLS/SCTP 按 5-tuple 转发；未知 ufrag / 未匹配 5-tuple 直接丢弃
- 会话状态机：Signaled(30s) → ICEChecking → Active(空闲120s→Idle) → Idle(300s 回收)

## 构建与运行

```bash
go build -o netherproxy.exe ./cmd/netherproxy

./netherproxy \
  -http :29132 \              # HTTP 信令监听地址（对外）
  -udp :29133 \               # UDP 数据面监听地址（对外，玩家复用此端口）
  -backend 127.0.0.1:19132 \  # 内网 BDS 信令地址
  -advertise 203.0.113.10 \    # 重写进 answer 的代理公网 IP（必填，填你的公网 IP）
  [-force-relay] \            # 同网段调试：占位重写 offer，防 BDS 直连旁路
  [-debug]                    # debug 日志（含 SDP 原文）
```

客户端添加服务器填 `<advertise>:<http端口>`。

## ⚠️ 部署要点（实测踩坑，BDS 1.26.60）

1. **数据面端口必须避开 BDS 端口池**：BDS 会把 19132–19139 预绑定到每张网卡
   地址上，操作系统按"更具体的绑定优先"投递 UDP——代理端口落在池内时，
   绑 `0.0.0.0` 也一个包都收不到。
2. **`m=` 行端口必须同步重写**：BDS answer 的 `m=` 是真实数据端口（不是占位符 9），
   与 candidate 不一致会导致客户端校验失败（`InitialConnection-83`）。
3. **同网段测试需 `-force-relay`**：否则 BDS 直接对 offer 里客户端的局域网
   candidate 反向建连，代理被旁路。公网部署无需开启。

## 验证记录

- ✅ BDS 1.26.60 (protocol 2211) + Windows GDK 客户端 1.26.60.24
- ✅ HTTP 信令透传 + SDP answer 重写 + 会话创建
- ✅ STUN ufrag 路由 + 客户端地址学习 + 5-tuple 转发
- ✅ DTLS 端到端（`a=setup:active`，fingerprint/identity 未动）
- ✅ 客户端成功进入游戏世界

## 项目结构

```
cmd/netherproxy/    入口
internal/
  session/          会话表（状态机 + 超时回收 + ufrag/5-tuple 双索引）
  signaling/        HTTP 透传 + 行级 SDP 重写（rewrite.go / proxy.go）
  dataplane/        单端口 UDP 转发（STUN 解析 + 两级路由）
docs/               NetherNet 协议全景与方案设计文档
```

测试：`go test ./...`

## 安全测试附记

`IdentityNotAllowed`（错误码 37）实测：BDS 在分配 UDP 端口**之前**强制校验
客户端身份（须微软签发的 token，自签被拒），匿名信令洪泛无法触及端口分配
逻辑——端口池耗尽攻击面在身份层已被封死。

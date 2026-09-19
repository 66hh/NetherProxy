// Package dataplane 实现 NetherProxy 的 UDP 单端口数据面。
//
// 公网仅暴露一个 UDP 端口承载全部玩家流量（文档 §7.3）：
//   - STUN 包：解析 USERNAME 属性，取冒号前半（服务端 ufrag）查会话表
//     路由，同时学习/更新客户端地址（5-tuple 软状态）；
//   - 非 STUN 包（DTLS/SCTP）：按客户端地址查反向索引转发，且必须匹配
//     先前经有效 STUN 建立的 5-tuple，否则丢弃（防伪造，文档 §8 风险 5）。
//
// 回包方向：每个会话一条通往 BDS 的内部 socket，BDS 回包经公网 socket
// 发回已学习的客户端地址。DTLS 全程端到端，代理只转发不终止。
package dataplane

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/pion/stun/v3"

	"NetherProxy/internal/session"
)

// stunMagicCookie 是 STUN 消息的魔数（RFC 5389），位于头部偏移 4。
const stunMagicCookie = 0x2112A442

// maxUDPPacketSize 是 UDP 载荷上限。
const maxUDPPacketSize = 65535

// Plane 是 UDP 数据面。
type Plane struct {
	public *net.UDPConn // 公网单端口 socket
	table  *session.Table
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Listen 在 addr（如 ":19133"）上监听 UDP 并启动转发循环。
func Listen(addr string, table *session.Table, log *slog.Logger) (*Plane, error) {
	if log == nil {
		log = slog.Default()
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	// 显式使用纯 IPv4 监听（0.0.0.0），排除双栈 socket 在部分 Windows
	// 环境下收不到网卡地址入站包的问题。
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return nil, err
	}
	p := &Plane{public: conn, table: table, log: log}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.wg.Add(1)
	go p.loop()
	p.log.Info("dataplane listening", "addr", conn.LocalAddr())
	return p, nil
}

// Port 返回公网监听端口（信令层重写 candidate 时使用）。
func (p *Plane) Port() uint16 {
	addr, ok := p.public.LocalAddr().(*net.UDPAddr)
	if !ok {
		return 0
	}
	return uint16(addr.Port)
}

// Close 关闭公网 socket 并停止转发循环。
func (p *Plane) Close() error {
	p.cancel()
	err := p.public.Close()
	p.wg.Wait()
	return err
}

// CreateSession 由信令层在拦截 answer 后调用：
// 建立通往 BDS 的内部 socket、注册会话、启动回包循环。
func (p *Plane) CreateSession(ufrag string, backendAddr netip.AddrPort) error {
	raddr := net.UDPAddrFromAddrPort(backendAddr)
	backend, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return err
	}
	sess := p.table.Add(ufrag, backendAddr, backend)
	p.wg.Add(1)
	go p.backendLoop(sess)
	return nil
}

// loop 是公网 socket 的收包循环：STUN 按 ufrag 路由并学习客户端地址，
// 其余按 5-tuple 反向索引转发。
func (p *Plane) loop() {
	defer p.wg.Done()
	buf := make([]byte, maxUDPPacketSize)
	for {
		n, src, err := p.public.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			p.log.Error("read public socket failed", "error", err)
			return
		}
		p.forwardClientToBackend(buf[:n], src)
	}
}

// forwardClientToBackend 转发一个客户端 -> BDS 方向的数据包。
func (p *Plane) forwardClientToBackend(pkt []byte, src netip.AddrPort) {
	if serverUfrag, ok := parseSTUNServerUfrag(pkt); ok {
		sess := p.table.ByUfrag(serverUfrag)
		if sess == nil {
			// 未知 ufrag：直接丢弃，不回应（入口 DDoS 缓解）。
			p.log.Debug("STUN with unknown ufrag dropped", "ufrag", serverUfrag, "src", src)
			return
		}
		p.table.BindClient(sess, src)
		if _, err := sess.Backend().Write(pkt); err != nil {
			p.log.Debug("forward STUN to backend failed", "ufrag", serverUfrag, "error", err)
		}
		return
	}

	// 非 STUN（DTLS/SCTP）：必须匹配已学习的 5-tuple。
	sess := p.table.ByClient(src)
	if sess == nil {
		p.log.Debug("non-STUN packet with unknown 5-tuple dropped", "src", src, "len", len(pkt))
		return
	}
	sess.Touch()
	if _, err := sess.Backend().Write(pkt); err != nil {
		p.log.Debug("forward data to backend failed", "client", src, "error", err)
	}
}

// backendLoop 是一个会话的回包循环：BDS -> 客户端。
// 内部 socket 关闭（会话回收）时退出。
func (p *Plane) backendLoop(sess *session.Session) {
	defer p.wg.Done()
	buf := make([]byte, maxUDPPacketSize)
	for {
		n, err := sess.Backend().Read(buf)
		if err != nil {
			return // 会话已关闭
		}
		client, ok := sess.Client()
		if !ok {
			continue // 客户端地址尚未学习，无法回包
		}
		if _, err := p.public.WriteToUDPAddrPort(buf[:n], client); err != nil {
			p.log.Debug("forward to client failed", "ufrag", sess.Ufrag, "error", err)
		}
	}
}

// parseSTUNServerUfrag 判定并解析 STUN 消息，返回 USERNAME 属性中冒号
// 前半的服务端 ufrag（路由键）。非 STUN、格式错误或缺少 USERNAME 时
// ok 为 false。
func parseSTUNServerUfrag(b []byte) (ufrag string, ok bool) {
	// 快速判定：长度 >= 20 且魔数匹配（文档附录 B）。
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return "", false
	}
	msg := &stun.Message{Raw: b}
	if err := msg.Decode(); err != nil {
		return "", false
	}
	var username stun.Username
	if err := username.GetFrom(msg); err != nil {
		return "", false
	}
	// USERNAME 格式："serverUfrag:clientUfrag"，取前半为路由键。
	server, _, found := strings.Cut(string(username), ":")
	if !found || server == "" {
		return "", false
	}
	return server, true
}

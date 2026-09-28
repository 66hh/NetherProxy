// Package multiplexer 实现 NetherProxy 的 UDP 单端口数据面。
//
// 公网仅暴露一个 UDP 端口承载全部玩家流量:
//   - 线路心跳包 (NPING): 直接回应 NPONG, 不进入会话转发
//   - STUN 包: 零分配解析 USERNAME 取服务端 ufrag 路由, 校验 MESSAGE-INTEGRITY
//     防伪造, 同时学习/更新客户端地址 (5-tuple 软状态)
//   - 非 STUN 包 (DTLS/SCTP): 按客户端地址查反向索引转发, 必须匹配先前经
//     有效 STUN 建立的 5-tuple, 否则丢弃
//
// 可靠性: 收包循环单包出错仅记录不退出; 每个 reader 带 recover 兜底,
// 单包处理 panic 不会拖垮整个数据面。
// 性能: 多 reader goroutine 并发收包 (跨平台), socket 缓冲调大, STUN 快速解析。
package multiplexer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/session"
)

// stunMagicCookie 是 STUN 消息的魔数（RFC 5389），位于头部偏移 4。
const stunMagicCookie = 0x2112A442

// maxUDPPacketSize 是 UDP 载荷上限。
const maxUDPPacketSize = 65535

// socketBufferSize 是公网 socket 的内核收发缓冲。
const socketBufferSize = 4 << 20

// maxReaders 是公网 socket 的并发收包 goroutine 数量上限。
const maxReaders = 8

// Multiplexer 是 UDP 数据面 (端口复用器)。
type Multiplexer struct {
	store   *conf.Store
	table   *session.Table
	tracker *EntryTracker

	public4 *net.UDPConn // 公网 IPv4 socket
	public6 *net.UDPConn // 公网 IPv6 socket (可选, 系统不支持时为空)

	lifeMu  sync.RWMutex // CreateSession 与 Close 的生命周期互斥
	closing atomic.Bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	totalRx atomic.Uint64 // 全局流量 (客户端 -> BDS)
	totalTx atomic.Uint64 // BDS -> 客户端
}

// TotalTraffic 返回全局累计转发流量 (字节)
func (m *Multiplexer) TotalTraffic() (rx, tx uint64) {
	return m.totalRx.Load(), m.totalTx.Load()
}

// New 创建数据面, 尚未开始监听。
func New(store *conf.Store, table *session.Table, tracker *EntryTracker) *Multiplexer {
	m := &Multiplexer{store: store, table: table, tracker: tracker}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m
}

// Start 监听配置的 UDP 地址并启动转发与回包循环。
//
// 地址族策略: 通配/空地址同时监听 IPv4 与 IPv6 两个独立 socket
// (不用双栈 socket, 避开部分 Windows 环境收不到网卡地址入站包的问题,
// 同时避免 4-in-6 映射地址导致 5-tuple 查找失配); 显式指定地址时
// 只监听对应协议。IPv6 不可用的环境自动降级为仅 IPv4。
func (m *Multiplexer) Start() error {
	cfg := m.store.Get().Multiplexer
	host := strings.TrimPrefix(strings.TrimSuffix(cfg.Host, "]"), "[")
	port := strconv.Itoa(cfg.Port)

	sockets := []struct {
		network  string
		required bool
	}{{"udp4", true}, {"udp6", false}}
	// 通配地址 (空/0.0.0.0/::) 不进入单栈分支: 用户写 0.0.0.0 意为"所有接口",
	// 应同时监听两个协议族
	if ip, err := netip.ParseAddr(host); err == nil && !ip.IsUnspecified() {
		if ip.Is4() {
			sockets = sockets[:1]
		} else {
			sockets = []struct {
				network  string
				required bool
			}{{"udp6", true}}
		}
	}

	for _, s := range sockets {
		listenHost := host
		if s.network == "udp6" {
			// 双栈场景下 v4 通配地址 (0.0.0.0) 不能用于 udp6, 换成 v6 通配
			if ip, err := netip.ParseAddr(host); err == nil && ip.Is4() {
				listenHost = "::"
			}
		}
		udpAddr, err := net.ResolveUDPAddr(s.network, net.JoinHostPort(listenHost, port))
		if err != nil {
			if s.required {
				return fmt.Errorf("resolve multiplexer addr: %w", err)
			}
			logger.Warn("ipv6 resolve unavailable, ipv4 only", "err", err)
			continue
		}
		conn, err := net.ListenUDP(s.network, udpAddr)
		if err != nil {
			if s.required {
				return fmt.Errorf("listen udp: %w", err)
			}
			logger.Warn("ipv6 listen unavailable, ipv4 only", "err", err)
			continue
		}
		if s.network == "udp6" {
			// 显式 v6only, 杜绝 v4 包以 4-in-6 地址进入 v6 socket
			setV6Only(conn)
		}
		if err := conn.SetReadBuffer(socketBufferSize); err != nil {
			logger.Warn("set udp read buffer failed", "err", err)
		}
		if err := conn.SetWriteBuffer(socketBufferSize); err != nil {
			logger.Warn("set udp write buffer failed", "err", err)
		}
		if s.network == "udp4" {
			m.public4 = conn
		} else {
			m.public6 = conn
		}
	}

	m.registerSessionMetrics()

	readers := min(runtime.NumCPU(), maxReaders)
	for _, conn := range []*net.UDPConn{m.public4, m.public6} {
		if conn == nil {
			continue
		}
		for i := 0; i < readers; i++ {
			m.wg.Add(1)
			go m.loop(conn)
		}
		logger.Info("multiplexer listening", "addr", conn.LocalAddr(), "readers", readers)
	}
	return nil
}

// Table 返回数据面使用的会话表。
func (m *Multiplexer) Table() *session.Table {
	return m.table
}

// Close 关闭公网 socket 并停止全部循环 (Start 失败未监听时调用也安全)。
func (m *Multiplexer) Close() error {
	m.lifeMu.Lock()
	m.closing.Store(true)
	m.lifeMu.Unlock()
	m.cancel()
	var err error
	if m.public4 != nil {
		err = m.public4.Close()
	}
	if m.public6 != nil {
		if e := m.public6.Close(); err == nil {
			err = e
		}
	}
	m.wg.Wait()
	return err
}

// CreateSession 由信令层在拦截 answer 后调用：
// 建立通往 BDS 的内部 socket、注册会话、启动回包循环。
func (m *Multiplexer) CreateSession(info session.SessionInfo) error {
	m.lifeMu.RLock()
	defer m.lifeMu.RUnlock()
	if m.closing.Load() {
		return errors.New("multiplexer is closing")
	}
	raddr := net.UDPAddrFromAddrPort(info.BackendAddr)
	backend, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return fmt.Errorf("dial backend: %w", err)
	}
	sess := m.table.Add(info, backend)
	if sess == nil {
		return errors.New("session table closed")
	}
	sessionActive.Inc()
	m.wg.Add(1)
	go m.backendLoop(sess)
	return nil
}

// loop 是公网 socket 的收包循环。单个包处理出错/panic 不退出循环;
// 连续读取失败按次退避, 防止永久 socket 错误时忙转刷日志。
func (m *Multiplexer) loop(conn *net.UDPConn) {
	defer m.wg.Done()
	buf := make([]byte, maxUDPPacketSize)
	errCount := 0
	for {
		n, src, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || m.ctx.Err() != nil {
				return
			}
			// 单个包读取失败 (如 ICMP 不可达回执) 不影响整体服务
			errCount++
			logger.Debug("read public socket failed", "err", err, "count", errCount)
			time.Sleep(min(time.Duration(errCount)*10*time.Millisecond, time.Second))
			continue
		}
		errCount = 0
		m.safeHandle(buf[:n], src)
	}
}

// safeHandle 带 recover 的包处理入口, 单包 panic 不会拖垮数据面。
func (m *Multiplexer) safeHandle(pkt []byte, src netip.AddrPort) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("packet handler panic", "err", r, "src", src)
		}
	}()
	m.handle(pkt, src)
}

// handle 处理一个客户端 -> BDS 方向的数据包。
func (m *Multiplexer) handle(pkt []byte, src netip.AddrPort) {
	// 线路心跳: 直接回应, 不转发到 BDS
	if isPingPacket(pkt) {
		m.replyPong(pkt, src)
		return
	}

	if ufrag, ok := parseSTUNServerUfrag(pkt); ok {
		sess := m.table.ByUfrag(ufrag)
		if sess == nil {
			// 未知 ufrag：直接丢弃，不回应（入口 DDoS 缓解）。
			DropCount("unknown_ufrag")
			logger.Debug("STUN with unknown ufrag dropped", "ufrag", ufrag, "src", src)
			return
		}
		// 安全校验: 携带 MESSAGE-INTEGRITY 的 STUN 必须通过 HMAC 校验 (密钥 = ice-pwd)
		ok, verified := verifySTUNIntegrity(pkt, sess.Pwd)
		if !ok {
			DropCount("integrity_failed")
			logger.Warn("STUN integrity check failed, dropped", "ufrag", ufrag, "src", src)
			return
		}
		// 只有通过完整性校验的包才允许学习/改绑客户端地址,
		// 未携带 MI 的包 (保活 indication 等) 只转发不改绑, 防止伪造地址劫持回包
		if verified {
			m.table.BindClient(sess, src)
		}
		sess.AddRx(len(pkt))
		m.totalRx.Add(uint64(len(pkt)))
		trafficBytes.WithLabelValues("rx").Add(float64(len(pkt)))
		if _, err := sess.Backend().Write(pkt); err != nil {
			logger.Debug("forward STUN to backend failed", "ufrag", ufrag, "err", err)
		}
		return
	}

	// 非 STUN（DTLS/SCTP）：必须匹配已学习的 5-tuple。
	sess := m.table.ByClient(src)
	if sess == nil {
		DropCount("unknown_tuple")
		logger.Debug("non-STUN packet with unknown 5-tuple dropped", "src", src, "len", len(pkt))
		return
	}
	sess.Touch()
	sess.AddRx(len(pkt))
	m.totalRx.Add(uint64(len(pkt)))
	trafficBytes.WithLabelValues("rx").Add(float64(len(pkt)))
	if _, err := sess.Backend().Write(pkt); err != nil {
		logger.Debug("forward data to backend failed", "client", src, "err", err)
	}
	// 玩家正常退出时的 DTLS close_notify: 送达后回收会话, 不等超时
	if isDTLSAlert(pkt) {
		logger.Info("client sent DTLS alert, closing session", "ufrag", sess.Ufrag, "player", sess.Player)
		m.table.RemoveByUfrag(sess.Ufrag)
	}
}

// backendLoop 是一个会话的回包循环：BDS -> 客户端。
// 内部 socket 关闭（会话回收）时退出。
func (m *Multiplexer) backendLoop(sess *session.Session) {
	defer m.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logger.Error("backend loop panic", "err", r, "ufrag", sess.Ufrag)
		}
	}()
	buf := make([]byte, maxUDPPacketSize)
	errCount := 0
	for {
		n, err := sess.Backend().Read(buf)
		if err != nil {
			// 仅会话关闭 (内部 socket 被关闭) 才退出; 瞬时错误
			// (如 Windows 收到 ICMP 不可达回执) 记录后继续, 避免回包通道静默死亡
			if errors.Is(err, net.ErrClosed) || sess.State() == session.StateClosed {
				return
			}
			errCount++
			logger.Debug("backend read failed", "ufrag", sess.Ufrag, "err", err, "count", errCount)
			if errCount >= 50 {
				// 持续报错 (如永久 WSAECONNRESET): 回收会话防空转刷屏
				logger.Warn("backend read keeps failing, closing session", "ufrag", sess.Ufrag, "err", err)
				m.table.RemoveByUfrag(sess.Ufrag)
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		errCount = 0
		client, ok := sess.Client()
		if !ok {
			continue // 客户端地址尚未学习，无法回包
		}
		sess.AddTx(n)
		m.totalTx.Add(uint64(n))
		trafficBytes.WithLabelValues("tx").Add(float64(n))
		if err := m.writePublic(buf[:n], client); err != nil {
			logger.Debug("forward to client failed", "ufrag", sess.Ufrag, "err", err)
		}
		if isDTLSAlert(buf[:n]) {
			logger.Info("backend sent DTLS alert, closing session", "ufrag", sess.Ufrag, "player", sess.Player)
			m.table.RemoveByUfrag(sess.Ufrag)
			return
		}
	}
}

// isDTLSAlert 检测加密 DTLS alert 记录 (epoch>0)。游戏中的加密 alert
// 几乎只有 close_notify 与 fatal alert, 均表示连接即将结束, 收到即回收会话。
//
// DTLS 记录头: type(1) version(2) epoch(2) seq(6) len(2)
func isDTLSAlert(b []byte) bool {
	return len(b) >= 13 && b[0] == 21 && binary.BigEndian.Uint16(b[3:5]) > 0
}

// parseSTUNServerUfrag 零分配解析 STUN 消息, 返回 USERNAME 属性中冒号
// 前半的服务端 ufrag (路由键)。非 STUN、格式错误或缺少 USERNAME 时 ok 为 false。
//
// 相比完整反序列化只做: 魔数判定 + TLV 扫描定位 USERNAME (0x0006)。
func parseSTUNServerUfrag(b []byte) (string, bool) {
	// 快速判定：长度 >= 20 且魔数匹配。
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return "", false
	}
	msgLen := int(binary.BigEndian.Uint16(b[2:4]))
	if msgLen%4 != 0 || 20+msgLen > len(b) {
		return "", false
	}
	attrs := b[20 : 20+msgLen]
	for len(attrs) >= 4 {
		typ := binary.BigEndian.Uint16(attrs[0:2])
		l := int(binary.BigEndian.Uint16(attrs[2:4]))
		if len(attrs) < 4+l {
			return "", false
		}
		if typ == 0x0006 { // USERNAME
			v := attrs[4 : 4+l]
			// USERNAME 格式："serverUfrag:clientUfrag"，取前半为路由键。
			if i := bytes.IndexByte(v, ':'); i > 0 {
				return string(v[:i]), true
			}
			return "", false
		}
		attrs = attrs[4+(l+3)&^3:] // 属性按 4 字节对齐
	}
	return "", false
}

// verifySTUNIntegrity 校验 STUN 消息的 MESSAGE-INTEGRITY (RFC 5389, HMAC-SHA1,
// 密钥 = 服务端 ice-pwd)。
//
// 返回值: ok 表示是否放行 (携带了属性但校验失败才判定为伪造),
// verified 表示是否实际通过了 MI 校验 (pwd 为空或消息未携带 MI 属性时为 false,
// 调用方不得据此改绑客户端地址)。
func verifySTUNIntegrity(b []byte, pwd string) (ok bool, verified bool) {
	if pwd == "" {
		return true, false
	}
	if len(b) < 20 {
		return false, false
	}
	msgLen := int(binary.BigEndian.Uint16(b[2:4]))
	if 20+msgLen > len(b) {
		return false, false
	}
	attrs := b[20 : 20+msgLen]
	off := 0
	for len(attrs)-off >= 4 {
		typ := binary.BigEndian.Uint16(attrs[off : off+2])
		l := int(binary.BigEndian.Uint16(attrs[off+2 : off+4]))
		if len(attrs)-off < 4+l {
			return false, false
		}
		if typ == 0x0008 { // MESSAGE-INTEGRITY
			if l != sha1.Size {
				return false, false
			}
			// RFC 5389 §15.4: 计算 HMAC 时 length 字段取到 MESSAGE-INTEGRITY
			// 属性结束为止, 消息内容为该属性之前的全部字节。
			var hdr [20]byte
			copy(hdr[:], b[:20])
			binary.BigEndian.PutUint16(hdr[2:4], uint16(off+24))
			mac := hmac.New(sha1.New, []byte(pwd))
			mac.Write(hdr[:])
			mac.Write(attrs[:off])
			if !hmac.Equal(mac.Sum(nil), attrs[off+4:off+4+l]) {
				return false, false
			}
			return true, true
		}
		off += 4 + (l+3)&^3
	}
	// 未携带 MESSAGE-INTEGRITY: 放行但不视为已验证
	return true, false
}

// writePublic 按目标地址族选择公网 socket 发送 (Unmap 防御 4-in-6 地址)。
func (m *Multiplexer) writePublic(pkt []byte, dst netip.AddrPort) error {
	conn := m.public4
	if dst.Addr().Unmap().Is6() {
		conn = m.public6
	}
	if conn == nil {
		return errors.New("no socket for address family")
	}
	_, err := conn.WriteToUDPAddrPort(pkt, dst)
	return err
}

// setV6Only 显式设置 IPV6_V6ONLY, 防止 v4 包以 4-in-6 映射地址进入 v6
// socket (Linux 默认关闭 v6only), 导致 5-tuple 查找失配丢包。
func setV6Only(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = setsockoptV6Only(fd)
	})
}

// EntryKey 返回线路的标识 (host:port)。
func EntryKey(e *conf.EntryConf) string {
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

package gateway

import (
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// splitDualListener 把 TCP listener 按首字节分流为明文与 TLS 两个 listener:
// TLS 握手记录首字节为 0x16 (handshake), 明文 HTTP 请求首字节为方法字母。
// 用于同端口同时支持 HTTP 与 HTTPS (安卓客户端仅 HTTP, iOS 仅 HTTPS)。
func splitDualListener(ln net.Listener, tlsCfg *tls.Config) (plain net.Listener, secure net.Listener) {
	p := newChanListener(ln.Addr())
	s := newChanListener(ln.Addr())
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				p.Close()
				s.Close()
				return
			}
			go dispatchDual(conn, p, s, tlsCfg)
		}
	}()
	return p, s
}

func dispatchDual(conn net.Conn, plain, secure *chanListener, tlsCfg *tls.Config) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	_, err := conn.Read(b[:])
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}
	wrapped := &prefixConn{Conn: conn, prefix: b[0], hasPrefix: true}
	if b[0] == 0x16 { // TLS handshake record
		secure.deliver(tls.Server(wrapped, tlsCfg))
		return
	}
	plain.deliver(wrapped)
}

// prefixConn 把分流时预读的首字节拼回读流
type prefixConn struct {
	net.Conn
	prefix    byte
	hasPrefix bool
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if c.hasPrefix {
		c.hasPrefix = false
		b[0] = c.prefix
		return 1, nil
	}
	return c.Conn.Read(b)
}

// chanListener 基于 channel 的 net.Listener
type chanListener struct {
	conns  chan net.Conn
	addr   net.Addr
	done   chan struct{}
	closed atomic.Bool
	once   sync.Once
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{conns: make(chan net.Conn), addr: addr, done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.once.Do(func() { l.closed.Store(true); close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return l.addr }

// deliver 投递连接, 已关闭时丢弃并关闭连接
func (l *chanListener) deliver(conn net.Conn) {
	if l.closed.Load() {
		_ = conn.Close()
		return
	}
	l.conns <- conn
}

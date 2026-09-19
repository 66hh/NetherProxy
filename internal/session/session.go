// Package session 实现 NetherProxy 的会话表。
//
// 每完成一次 SDP 交换产生一个会话：以 answer 中的服务端 ice-ufrag 为路由键，
// 记录内网 BDS 为该连接分配的 UDP 地址，并持有一条通往该地址的内部 socket。
// 数据面先按 STUN USERNAME（ufrag）路由并学习客户端地址，之后按客户端地址
// （5-tuple 软状态）转发 DTLS/SCTP 流量。参见文档 §7.5。
package session

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// State 表示会话生命周期状态（文档 §7.5 状态机）。
type State uint8

const (
	// StateSignaled 表示 SDP 交换已完成，等待首个 STUN 包。
	StateSignaled State = iota
	// StateICEChecking 表示已收到首个 STUN，等待 DTLS 流量。
	StateICEChecking
	// StateActive 表示已观察到 DTLS/SCTP 数据流量。
	StateActive
	// StateIdle 表示曾经活跃但已空闲。
	StateIdle
	// StateClosed 表示会话已关闭并等待回收。
	StateClosed
)

// String 返回状态的可读名称。
func (s State) String() string {
	switch s {
	case StateSignaled:
		return "Signaled"
	case StateICEChecking:
		return "ICEChecking"
	case StateActive:
		return "Active"
	case StateIdle:
		return "Idle"
	case StateClosed:
		return "Closed"
	}
	return fmt.Sprintf("State(%d)", uint8(s))
}

// 超时参数（文档 §7.5）。
const (
	// SignaledTimeout 是 Signaled 状态等待首个 STUN 的超时。
	SignaledTimeout = 30 * time.Second
	// ActiveIdleTimeout 是 Active 会话空闲多久后转入 Idle。
	ActiveIdleTimeout = 120 * time.Second
	// IdleReapTimeout 是 Idle 会话多久后被回收。
	IdleReapTimeout = 300 * time.Second
	// TupleStaleTimeout 是客户端地址软状态的有效期。
	TupleStaleTimeout = 60 * time.Second
)

// ErrClosed 表示会话已经关闭。
var ErrClosed = errors.New("session: session closed")

// Session 表示一条已协商的 NetherNet 连接。
//
// 除标注 Locked 的方法外，所有方法可并发调用。
type Session struct {
	// Ufrag 是服务端 answer 中的 ice-ufrag，连接级唯一，作为路由键。
	Ufrag string
	// BackendAddr 是 BDS 为该连接分配的 UDP 地址（answer 中的 candidate）。
	BackendAddr netip.AddrPort

	backend *net.UDPConn // 通往 BackendAddr 的内部 socket

	mu           sync.RWMutex
	state        State
	client       netip.AddrPort // 学习到的客户端地址（5-tuple 远端）
	hasClient    bool
	created      time.Time
	lastActivity time.Time // 最近一次数据面活动
	lastTuple    time.Time // 客户端地址最近一次被（重新）学习的时间
	closed       bool
}

// Backend 返回通往 BDS 的内部 socket。会话关闭后写入会失败。
func (s *Session) Backend() *net.UDPConn { return s.backend }

// Client 返回已学习的客户端地址；尚未学习时 ok 为 false。
func (s *Session) Client() (addr netip.AddrPort, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client, s.hasClient && !s.closed
}

// State 返回当前会话状态。
func (s *Session) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// LearnClient 在收到携带本会话 ufrag 的有效 STUN 包时调用：
// 记录或更新客户端地址（客户端换网时重新学习，路由不断），
// 并将 Signaled 会话推进到 ICEChecking。返回地址是否发生变化。
func (s *Session) LearnClient(addr netip.AddrPort) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	changed = !s.hasClient || s.client != addr
	s.client, s.hasClient = addr, true
	now := time.Now()
	s.lastTuple = now
	s.lastActivity = now
	if s.state == StateSignaled {
		s.state = StateICEChecking
	}
	return changed
}

// Touch 记录一次非 STUN 数据面活动（DTLS/SCTP），把会话推进到 Active。
func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActivity = time.Now()
	if s.state < StateActive {
		s.state = StateActive
	}
}

// TupleFresh 报告已学习的客户端地址软状态是否仍在有效期内。
func (s *Session) TupleFresh() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasClient && !s.closed && time.Since(s.lastTuple) < TupleStaleTimeout
}

// MatchesClient 报告 addr 是否为当前生效的客户端地址（校验 5-tuple 软状态）。
func (s *Session) MatchesClient(addr netip.AddrPort) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasClient && !s.closed && s.client == addr &&
		time.Since(s.lastTuple) < TupleStaleTimeout
}

// close 关闭内部 socket 并将会话标记为 Closed。幂等。
func (s *Session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.state = StateClosed
	_ = s.backend.Close()
}

// reap 依据状态机执行超时判定，返回是否应回收该会话。
// 仅在 Table 的 reaper 中调用。
func (s *Session) reap(now time.Time) (closeIt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	switch s.state {
	case StateSignaled:
		if now.Sub(s.created) > SignaledTimeout {
			return true
		}
	case StateICEChecking, StateActive:
		if now.Sub(s.lastActivity) > ActiveIdleTimeout {
			s.state = StateIdle
		}
	case StateIdle:
		if now.Sub(s.lastActivity) > IdleReapTimeout {
			return true
		}
	}
	return false
}

// Table 是会话表，提供 ufrag 与客户端地址两级索引。
//
// 一致性说明：byClient 反向索引允许短暂滞后——查找时会用
// Session.MatchesClient 复核，滞后的条目被视为不存在并顺手清理。
type Table struct {
	log *slog.Logger

	mu       sync.RWMutex
	byUfrag  map[string]*Session
	byClient map[netip.AddrPort]*Session

	reapInterval time.Duration
	closed       chan struct{}
	once         sync.Once
}

// NewTable 创建会话表并启动后台回收 goroutine。
func NewTable(log *slog.Logger) *Table {
	if log == nil {
		log = slog.Default()
	}
	t := &Table{
		log:          log,
		byUfrag:      make(map[string]*Session),
		byClient:     make(map[netip.AddrPort]*Session),
		reapInterval: 10 * time.Second,
		closed:       make(chan struct{}),
	}
	go t.reaper()
	return t
}

// Add 注册一个已完成信令交换的新会话。backend 是通往 BackendAddr 的
// 已连接 UDP socket，所有权移交给会话。相同 ufrag 的既有会话会被替换。
func (t *Table) Add(ufrag string, backendAddr netip.AddrPort, backend *net.UDPConn) *Session {
	s := &Session{
		Ufrag:       ufrag,
		BackendAddr: backendAddr,
		backend:     backend,
		state:       StateSignaled,
		created:     time.Now(),
		lastActivity: time.Now(),
	}
	t.mu.Lock()
	if old := t.byUfrag[ufrag]; old != nil {
		t.log.Warn("replacing session with duplicate ufrag", "ufrag", ufrag)
		t.removeLocked(old)
		old.close()
	}
	t.byUfrag[ufrag] = s
	t.mu.Unlock()
	t.log.Debug("session created", "ufrag", ufrag, "backend", backendAddr)
	return s
}

// ByUfrag 按服务端 ice-ufrag 查找会话；不存在或已关闭时返回 nil。
func (t *Table) ByUfrag(ufrag string) *Session {
	t.mu.RLock()
	s := t.byUfrag[ufrag]
	t.mu.RUnlock()
	if s == nil || s.State() == StateClosed {
		return nil
	}
	return s
}

// ByClient 按已学习的客户端地址查找会话，并复核软状态一致性。
// 滞后或过期的条目被清理并视为不存在。
func (t *Table) ByClient(addr netip.AddrPort) *Session {
	t.mu.RLock()
	s := t.byClient[addr]
	t.mu.RUnlock()
	if s == nil {
		return nil
	}
	if !s.MatchesClient(addr) {
		t.mu.Lock()
		if t.byClient[addr] == s {
			delete(t.byClient, addr)
		}
		t.mu.Unlock()
		return nil
	}
	return s
}

// BindClient 学习/更新会话的客户端地址并维护反向索引。
// 客户端换网时旧映射被移除，新地址接管路由。
func (t *Table) BindClient(s *Session, addr netip.AddrPort) {
	old, ok := s.Client()
	changed := s.LearnClient(addr)
	if !changed {
		return
	}
	t.mu.Lock()
	if ok {
		if cur := t.byClient[old]; cur == s {
			delete(t.byClient, old)
		}
	}
	t.byClient[addr] = s
	t.mu.Unlock()
	t.log.Debug("client address learned", "ufrag", s.Ufrag, "client", addr)
}

// Len 返回当前活跃会话数（含各状态，不含已关闭）。
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byUfrag)
}

// Close 停止后台回收并关闭全部会话。
func (t *Table) Close() {
	t.once.Do(func() {
		close(t.closed)
		t.mu.Lock()
		for _, s := range t.byUfrag {
			s.close()
		}
		t.byUfrag = make(map[string]*Session)
		t.byClient = make(map[netip.AddrPort]*Session)
		t.mu.Unlock()
	})
}

// removeLocked 从两级索引中移除会话。调用者须持有 t.mu。
func (t *Table) removeLocked(s *Session) {
	delete(t.byUfrag, s.Ufrag)
	if addr, ok := s.Client(); ok && t.byClient[addr] == s {
		delete(t.byClient, addr)
	}
}

// reaper 周期性扫描会话表，按状态机执行超时转移与回收。
func (t *Table) reaper() {
	ticker := time.NewTicker(t.reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.closed:
			return
		case now := <-ticker.C:
			t.reap(now)
		}
	}
}

func (t *Table) reap(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ufrag, s := range t.byUfrag {
		if !s.reap(now) {
			continue
		}
		t.log.Debug("session reaped", "ufrag", ufrag, "backend", s.BackendAddr)
		t.removeLocked(s)
		s.close()
	}
}

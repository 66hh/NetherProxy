// Package session 实现 NetherProxy 的会话表。
//
// 每完成一次 SDP 交换产生一个会话：以 answer 中的服务端 ice-ufrag 为路由键，
// 记录内网 BDS 为该连接分配的 UDP 地址，并持有一条通往该地址的内部 socket。
// 数据面先按 STUN USERNAME（ufrag）路由并学习客户端地址，之后按客户端地址
// （5-tuple 软状态）转发 DTLS/SCTP 流量。
package session

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
)

// State 表示会话生命周期状态。
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

// 超时参数由 conf.SessionConf 配置, 每次判定从配置中心读取, 热更即时生效

// ErrClosed 表示会话已经关闭。
var ErrClosed = errors.New("session: session closed")

// Session 表示一条已协商的 NetherNet 连接。
//
// 除标注 Locked 的方法外，所有方法可并发调用。
type Session struct {
	// Ufrag 是服务端 answer 中的 ice-ufrag，连接级唯一，作为路由键。
	Ufrag string
	// Pwd 是服务端 answer 中的 ice-pwd, 用于校验客户端 STUN 包的 MESSAGE-INTEGRITY。
	Pwd string
	// Player 是 offer 中经验证的玩家名 (identity xname)。
	Player string
	// XUID 是玩家的 Xbox 用户 ID (identity xid)。
	XUID string
	// BackendAddr 是 BDS 为该连接分配的 UDP 地址（answer 中的 candidate）。
	BackendAddr netip.AddrPort
	// Entry 是分配给该会话的公网线路标识 (host:port)。
	Entry string

	backend *net.UDPConn // 通往 BackendAddr 的内部 socket

	// 流量统计, 原子计数
	rxBytes atomic.Uint64 // 客户端 -> BDS
	txBytes atomic.Uint64 // BDS -> 客户端

	mu           sync.RWMutex
	state        State
	client       netip.AddrPort // 学习到的客户端地址（5-tuple 远端）
	hasClient    bool
	created      time.Time
	lastActivity time.Time // 最近一次数据面活动
	lastTuple    time.Time // 客户端地址最近一次被（重新）学习的时间
	closed       bool
}

// AddRx 统计客户端 -> BDS 方向的字节数。
func (s *Session) AddRx(n int) {
	s.rxBytes.Add(uint64(n))
}

// AddTx 统计 BDS -> 客户端方向的字节数。
func (s *Session) AddTx(n int) {
	s.txBytes.Add(uint64(n))
}

// Traffic 返回双向流量字节数 (rx: 客户端->BDS, tx: BDS->客户端)。
func (s *Session) Traffic() (rx, tx uint64) {
	return s.rxBytes.Load(), s.txBytes.Load()
}

// Created 返回会话创建时间。
func (s *Session) Created() time.Time {
	return s.created
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

// Touch 记录一次非 STUN 数据面活动（DTLS/SCTP）：刷新活动时间并把会话
// 推进到 Active（含 Idle 回迁）; 已学习客户端地址时同步续期 5-tuple 软状态,
// 防止活跃会话因长时间无 STUN 保活被误判过期。
func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	now := time.Now()
	s.lastActivity = now
	if s.hasClient {
		s.lastTuple = now
	}
	if s.state != StateActive {
		s.state = StateActive
	}
}

// MatchesClient 报告 addr 是否为当前生效的客户端地址（校验 5-tuple 软状态）。
func (s *Session) MatchesClient(addr netip.AddrPort, staleTimeout time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hasClient && !s.closed && s.client == addr &&
		time.Since(s.lastTuple) < staleTimeout
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
func (s *Session) reap(now time.Time, cfg conf.SessionConf) (closeIt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	switch s.state {
	case StateSignaled:
		if now.Sub(s.created) > cfg.Signaled() {
			return true
		}
	case StateICEChecking, StateActive:
		if now.Sub(s.lastActivity) > cfg.ActiveIdle() {
			s.state = StateIdle
		}
	case StateIdle:
		if now.Sub(s.lastActivity) > cfg.IdleReap() {
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
	store    *conf.Store
	mu       sync.RWMutex
	byUfrag  map[string]*Session
	byClient map[netip.AddrPort]*Session

	// onRemove 在会话从表中移除后依次调用 (锁外执行), 用于释放关联资源如线路计数。
	// 仅在启动期经 AddOnRemove 注册。
	onRemove []func(*Session)

	reapInterval time.Duration
	closed       chan struct{}
	closedFlag   atomic.Bool
	once         sync.Once
}

// NewTable 创建会话表并启动后台回收 goroutine。
func NewTable(store *conf.Store) *Table {
	t := &Table{
		store:        store,
		byUfrag:      make(map[string]*Session),
		byClient:     make(map[netip.AddrPort]*Session),
		reapInterval: 10 * time.Second,
		closed:       make(chan struct{}),
	}
	go t.reaper()
	return t
}

// SessionInfo 是创建会话所需的信息
type SessionInfo struct {
	Ufrag       string         // 服务端 ice-ufrag (路由键)
	Pwd         string         // 服务端 ice-pwd (STUN 完整性校验密钥)
	Player      string         // 玩家名
	XUID        string         // Xbox 用户 ID
	BackendAddr netip.AddrPort // BDS 分配的 UDP 地址
	Entry       string         // 分配的公网线路 (host:port)
}

// Add 注册一个已完成信令交换的新会话。backend 是通往 BackendAddr 的
// 已连接 UDP socket，所有权移交给会话。相同 ufrag 的既有会话会被替换。
func (t *Table) Add(info SessionInfo, backend *net.UDPConn) *Session {
	if t.closedFlag.Load() {
		_ = backend.Close()
		logger.Warn("session table closed, rejecting add", "ufrag", info.Ufrag)
		return nil
	}
	s := &Session{
		Ufrag:        info.Ufrag,
		Pwd:          info.Pwd,
		Player:       info.Player,
		XUID:         info.XUID,
		BackendAddr:  info.BackendAddr,
		Entry:        info.Entry,
		backend:      backend,
		state:        StateSignaled,
		created:      time.Now(),
		lastActivity: time.Now(),
	}
	t.mu.Lock()
	if t.closedFlag.Load() {
		// Close 已完成的窗口期, 拒绝插入 (reaper 已停, 无人回收)
		t.mu.Unlock()
		_ = backend.Close()
		return nil
	}
	old := t.byUfrag[info.Ufrag]
	if old != nil {
		t.removeLocked(old)
	}
	t.byUfrag[info.Ufrag] = s
	t.mu.Unlock()

	if old != nil {
		logger.Warn("replacing session with duplicate ufrag", "ufrag", info.Ufrag)
		old.close()
		t.notifyRemove(old)
	}
	logger.Debug("session created", "ufrag", info.Ufrag, "player", info.Player, "backend", info.BackendAddr, "entry", info.Entry)
	return s
}

// RemoveByUfrag 移除并关闭指定会话 (API 掐断), 不存在返回 false
func (t *Table) RemoveByUfrag(ufrag string) bool {
	t.mu.Lock()
	s := t.byUfrag[ufrag]
	if s != nil {
		t.removeLocked(s)
	}
	t.mu.Unlock()
	if s == nil {
		return false
	}
	s.close()
	t.notifyRemove(s)
	return true
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
	if !s.MatchesClient(addr, t.store.Get().Session.TupleStale()) {
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
	if t.byUfrag[s.Ufrag] != s {
		// 会话已被移除/替换, 不回插反向索引
		t.mu.Unlock()
		return
	}
	if ok {
		if cur := t.byClient[old]; cur == s {
			delete(t.byClient, old)
		}
	}
	if prev := t.byClient[addr]; prev != nil && prev != s {
		logger.Warn("client address taken over by new session", "addr", addr, "old_ufrag", prev.Ufrag, "new_ufrag", s.Ufrag)
	}
	t.byClient[addr] = s
	t.mu.Unlock()
	logger.Debug("client address learned", "ufrag", s.Ufrag, "client", addr)
}

// Len 返回当前活跃会话数（含各状态，不含已关闭）。
func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byUfrag)
}

// Range 遍历全部活跃会话, fn 不得修改会话表。
func (t *Table) Range(fn func(*Session)) {
	t.mu.RLock()
	sessions := make([]*Session, 0, len(t.byUfrag))
	for _, s := range t.byUfrag {
		sessions = append(sessions, s)
	}
	t.mu.RUnlock()
	for _, s := range sessions {
		fn(s)
	}
}

// Close 停止后台回收并关闭全部会话。
func (t *Table) Close() {
	t.once.Do(func() {
		t.closedFlag.Store(true)
		close(t.closed)
		t.mu.Lock()
		sessions := make([]*Session, 0, len(t.byUfrag))
		for _, s := range t.byUfrag {
			sessions = append(sessions, s)
		}
		t.byUfrag = make(map[string]*Session)
		t.byClient = make(map[netip.AddrPort]*Session)
		t.mu.Unlock()
		for _, s := range sessions {
			s.close()
			t.notifyRemove(s)
		}
	})
}

// removeLocked 从两级索引中移除会话。调用者须持有 t.mu。
func (t *Table) removeLocked(s *Session) {
	delete(t.byUfrag, s.Ufrag)
	if addr, ok := s.Client(); ok && t.byClient[addr] == s {
		delete(t.byClient, addr)
	}
}

func (t *Table) notifyRemove(s *Session) {
	t.mu.RLock()
	fns := t.onRemove
	t.mu.RUnlock()
	for _, fn := range fns {
		fn(s)
	}
}

// AddOnRemove 注册会话移除回调, 仅在启动期调用
func (t *Table) AddOnRemove(fn func(*Session)) {
	t.mu.Lock()
	t.onRemove = append(t.onRemove, fn)
	t.mu.Unlock()
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
	var removed []*Session
	cfg := t.store.Get().Session
	t.mu.Lock()
	for _, s := range t.byUfrag {
		if !s.reap(now, cfg) {
			continue
		}
		t.removeLocked(s)
		removed = append(removed, s)
	}
	t.mu.Unlock()
	for _, s := range removed {
		logger.Debug("session reaped", "ufrag", s.Ufrag, "backend", s.BackendAddr, "entry", s.Entry)
		s.close()
		t.notifyRemove(s)
	}
}

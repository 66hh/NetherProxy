package multiplexer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
)

// 线路心跳包: 代理主动发往线路的可达性探测, 数据面识别后直接回应 NPONG,
// 不进入会话转发逻辑 (不转发到 BDS)
const (
	pingMagic  = "NPING1"
	pongMagic  = "NPONG1"
	nonceSize  = 16
	packetSize = len(pingMagic) + nonceSize
)

var (
	entryHealthy = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "entry",
		Name:      "healthy",
		Help:      "Whether the entry line is currently routable (1) or offline (0).",
	}, []string{"entry"})

	entryHeartbeatTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "entry",
		Name:      "heartbeat_total",
		Help:      "Total number of heartbeat probes per entry line.",
	}, []string{"entry", "result"})

	entryHeartbeatRTT = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "entry",
		Name:      "heartbeat_rtt_seconds",
		Help:      "Last successful heartbeat round-trip time in seconds.",
	}, []string{"entry"})
)

func init() {
	prometheus.MustRegister(entryHealthy, entryHeartbeatTotal, entryHeartbeatRTT)
}

func isPingPacket(pkt []byte) bool {
	return len(pkt) == packetSize && bytes.HasPrefix(pkt, []byte(pingMagic))
}

func makePong(ping []byte) []byte {
	pong := make([]byte, packetSize)
	copy(pong, pongMagic)
	copy(pong[len(pongMagic):], ping[len(pingMagic):])
	return pong
}

func isPongFor(resp, ping []byte) bool {
	return len(resp) == packetSize &&
		bytes.HasPrefix(resp, []byte(pongMagic)) &&
		bytes.Equal(resp[len(pongMagic):], ping[len(pingMagic):])
}

// replyPong 回应线路心跳
func (m *Multiplexer) replyPong(ping []byte, src netip.AddrPort) {
	if _, err := m.public.WriteToUDPAddrPort(makePong(ping), src); err != nil {
		logger.Debug("reply pong failed", "src", src, "err", err)
	}
}

// ProbeEvent 一次探测结果 (时间 + 成败), 用于面板状态历史图
type ProbeEvent struct {
	Time time.Time `json:"time"`
	OK   bool      `json:"ok"`
}

// EntryStats 线路心跳统计
type EntryStats struct {
	Healthy          bool         `json:"healthy"`
	TotalProbes      uint64       `json:"total_probes"`
	FailedProbes     uint64       `json:"failed_probes"`
	ConsecutiveFails int          `json:"consecutive_fails"`
	LastRTTMs        float64      `json:"last_rtt_ms"`
	LastError        string       `json:"last_error,omitempty"`
	LastChange       time.Time    `json:"last_change"`
	History          []ProbeEvent `json:"history,omitempty"` // 探测结果时间线, 用于状态图

	unresponsive bool // 已记录过"无响应"日志, 避免刷屏
}

// EntryTracker 跟踪各线路的心跳统计与可用状态, 统计持久化到独立 JSON 文件
type EntryTracker struct {
	store *conf.Store
	mu    sync.RWMutex
	stats map[string]*EntryStats

	path      string // 统计持久化文件路径
	dirty     bool   // 有待落盘的变更
	urgent    bool   // 状态变化等关键变更, 立即落盘
	lastFlush time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

// NewEntryTracker 创建跟踪器, 加载既有统计并启动后台落盘循环
func NewEntryTracker(path string, store *conf.Store) *EntryTracker {
	t := &EntryTracker{store: store, stats: make(map[string]*EntryStats), path: path}
	t.load()
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})
	go t.flushLoop(ctx)
	return t
}

// Close 停止后台落盘并写入最终统计
func (t *EntryTracker) Close() {
	t.cancel()
	<-t.done
	t.flush()
}

// load 启动时加载既有统计 (如上次运行的累计探测数)
func (t *EntryTracker) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return // 文件不存在等: 从零开始
	}
	var loaded map[string]EntryStats
	if err := json.Unmarshal(data, &loaded); err != nil {
		logger.Warn("load entry stats failed, starting fresh", "path", t.path, "err", err)
		return
	}
	for k, v := range loaded {
		// 累计计数保留, 但当前失败状态清零 (重启后重新探测判定)
		v.ConsecutiveFails = 0
		v.unresponsive = false
		v.Healthy = true
		t.stats[k] = &v
		entryHealthy.WithLabelValues(k).Set(1)
		entryHeartbeatRTT.WithLabelValues(k).Set(v.LastRTTMs / 1000)
	}
}

// markDirty 标记有待落盘的变更 (30s 合并落盘)。调用者须持有锁。
func (t *EntryTracker) markDirty() {
	t.dirty = true
}

// markUrgent 标记关键变更 (状态切换等), 立即落盘。调用者须持有锁。
func (t *EntryTracker) markUrgent() {
	t.dirty = true
	t.urgent = true
}

// flushLoop 周期性将变更落盘: 状态变化等关键变更立即写,
// 纯计数变更 30 秒合并一次, 避免高频重写文件
func (t *EntryTracker) flushLoop(ctx context.Context) {
	defer close(t.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			interval, err := time.ParseDuration(t.store.Get().Stats.FlushInterval)
			if err != nil || interval <= 0 {
				interval = 30 * time.Second
			}
			t.mu.Lock()
			need := t.dirty && (t.urgent || time.Since(t.lastFlush) > interval)
			t.mu.Unlock()
			if need {
				t.flush()
			}
		}
	}
}

// flush 有变更时将统计写入 JSON 文件 (先写临时文件再替换, 避免截断)
func (t *EntryTracker) flush() {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return
	}
	t.dirty = false
	t.urgent = false
	t.lastFlush = time.Now()
	snap := make(map[string]EntryStats, len(t.stats))
	for k, s := range t.stats {
		snap[k] = *s
	}
	t.mu.Unlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		logger.Error("marshal entry stats failed", "err", err)
		return
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logger.Error("write entry stats failed", "path", t.path, "err", err)
		t.reDirty()
		return
	}
	if err := os.Rename(tmp, t.path); err != nil {
		logger.Error("replace entry stats failed", "path", t.path, "err", err)
		t.reDirty()
	}
}

// reDirty 写失败后重新置脏, 下个 tick 重试
func (t *EntryTracker) reDirty() {
	t.mu.Lock()
	t.dirty = true
	t.mu.Unlock()
}

// statsHistorySize 返回配置的状态历史上限
func (t *EntryTracker) statsHistorySize() int {
	return t.store.Get().Stats.StatusHistorySize
}

// Healthy 报告线路是否可参与路由 (未知线路默认可用)
func (t *EntryTracker) Healthy(key string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.stats[key]
	return s == nil || s.Healthy
}

// appendHistory 追加一次探测结果, 超出上限 (stats.status_history_size) 丢弃最旧。
// 调用者须持有锁。
func (s *EntryStats) appendHistory(ok bool, maxSize int) {
	if maxSize <= 0 {
		maxSize = 500
	}
	s.History = append(s.History, ProbeEvent{Time: time.Now(), OK: ok})
	if len(s.History) > maxSize {
		s.History = s.History[len(s.History)-maxSize:]
	}
}

// Record 记录一次探测结果。autoOffline 为 true 且连续失败达到 retries 时
// 将线路标记为下线; 否则仅记录统计与日志, 不影响路由。
func (t *EntryTracker) Record(key string, rtt time.Duration, probeErr error, manualOnly bool, retries int) {
	t.mu.Lock()
	s := t.stats[key]
	if s == nil {
		s = &EntryStats{Healthy: true, LastChange: time.Now()}
		t.stats[key] = s
	}
	s.TotalProbes++
	s.appendHistory(probeErr == nil, t.statsHistorySize())
	t.markDirty()

	if probeErr != nil {
		s.FailedProbes++
		s.ConsecutiveFails++
		s.LastError = probeErr.Error()
		entryHeartbeatTotal.WithLabelValues(key, "failure").Inc()
		if s.ConsecutiveFails >= retries && !s.unresponsive {
			s.unresponsive = true
			if !manualOnly && s.Healthy {
				s.Healthy = false
				s.LastChange = time.Now()
				t.markUrgent()
				entryHealthy.WithLabelValues(key).Set(0)
			}
			logger.Error("entry unresponsive", "entry", key,
				"consecutive_fails", s.ConsecutiveFails, "manual_only", manualOnly, "err", probeErr)
		}
		t.mu.Unlock()
		return
	}

	s.ConsecutiveFails = 0
	s.LastError = ""
	s.LastRTTMs = float64(rtt.Microseconds()) / 1000
	entryHeartbeatTotal.WithLabelValues(key, "success").Inc()
	entryHeartbeatRTT.WithLabelValues(key).Set(rtt.Seconds())
	if s.unresponsive {
		s.unresponsive = false
		logger.Info("entry responsive again", "entry", key, "rtt_ms", s.LastRTTMs)
	}
	if !s.Healthy {
		s.Healthy = true
		s.LastChange = time.Now()
		t.markUrgent()
		entryHealthy.WithLabelValues(key).Set(1)
		logger.Info("entry back online", "entry", key)
	}
	t.mu.Unlock()
}

// Remove 移除线路统计并清理 Prometheus 序列 (线路从配置中删除时调用)
func (t *EntryTracker) Remove(key string) {
	t.mu.Lock()
	delete(t.stats, key)
	t.markDirty()
	t.mu.Unlock()
	entryHealthy.DeleteLabelValues(key)
	entryHeartbeatTotal.DeleteLabelValues(key, "success")
	entryHeartbeatTotal.DeleteLabelValues(key, "failure")
	entryHeartbeatRTT.DeleteLabelValues(key)
}

// Snapshot 返回全部线路统计的副本 (History 深拷贝, 防读写竞态)
func (t *EntryTracker) Snapshot() map[string]EntryStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]EntryStats, len(t.stats))
	for k, s := range t.stats {
		cp := *s
		cp.History = append([]ProbeEvent(nil), s.History...)
		out[k] = cp
	}
	return out
}

// Heartbeat 管理全部线路的主动探测 worker, 随配置热更新增删
type Heartbeat struct {
	store   *conf.Store
	tracker *EntryTracker

	mu      sync.Mutex
	workers map[string]*hbWorker
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewHeartbeat(store *conf.Store, tracker *EntryTracker) *Heartbeat {
	return &Heartbeat{store: store, tracker: tracker, workers: make(map[string]*hbWorker)}
}

// Start 启动心跳管理器
func (h *Heartbeat) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go h.reconcileLoop(ctx)
}

// Close 停止全部心跳 worker 并等待退出
func (h *Heartbeat) Close() {
	h.cancel()
	<-h.done
	h.mu.Lock()
	workers := make([]*hbWorker, 0, len(h.workers))
	for _, w := range h.workers {
		workers = append(workers, w)
	}
	h.workers = make(map[string]*hbWorker)
	h.mu.Unlock()
	for _, w := range workers {
		w.stop()
	}
}

// reconcileLoop 周期性对比配置与运行中的 worker
func (h *Heartbeat) reconcileLoop(ctx context.Context) {
	defer close(h.done)
	h.reconcile(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcile(ctx)
		}
	}
}

// reconcile 增删 worker 使探测集合与配置一致, 配置变更的 worker 原地重启
func (h *Heartbeat) reconcile(ctx context.Context) {
	want := make(map[string]conf.EntryConf)
	exists := make(map[string]bool)
	cfg := h.store.Get()
	for i := range cfg.Entry {
		e := &cfg.Entry[i]
		exists[EntryKey(e)] = true
		if e.Enable && e.Heartbeat.Enable {
			want[EntryKey(e)] = *e
		}
	}

	// 清理已从配置中删除的线路的历史统计与指标序列
	for key := range h.tracker.Snapshot() {
		if !exists[key] {
			h.tracker.Remove(key)
		}
	}

	h.mu.Lock()
	var toStop []*hbWorker
	for key, w := range h.workers {
		e, ok := want[key]
		if !ok {
			delete(h.workers, key)
			toStop = append(toStop, w)
			continue
		}
		if w.entry != e {
			delete(h.workers, key)
			// 锁内先标记停止, 旧 worker 在途探测立即丢弃, 不与新 worker 双份探测
			w.mu.Lock()
			w.isStopped = true
			w.mu.Unlock()
			toStop = append(toStop, w)
			nw := newHBWorker(key, e, h.tracker)
			h.workers[key] = nw
			nw.start(ctx)
			logger.Debug("heartbeat worker restarted", "entry", key)
		}
	}
	for key, e := range want {
		if _, ok := h.workers[key]; !ok {
			w := newHBWorker(key, e, h.tracker)
			h.workers[key] = w
			w.start(ctx)
			logger.Info("heartbeat started", "entry", key,
				"interval", e.Heartbeat.Interval, "manual_only", e.Heartbeat.ManualOnly)
		}
	}
	h.mu.Unlock()

	// stop 在锁外执行 (可能等待进行中的探测)
	for _, w := range toStop {
		w.stop()
	}
}

// hbWorker 是单条线路的探测 worker
type hbWorker struct {
	key     string
	entry   conf.EntryConf
	tracker *EntryTracker
	cancel  context.CancelFunc
	done    chan struct{}

	mu        sync.Mutex
	conn      *net.UDPConn // 进行中的 probe 使用的连接, stop 时关闭以打断阻塞的 Read
	isStopped bool
}

func newHBWorker(key string, entry conf.EntryConf, tracker *EntryTracker) *hbWorker {
	return &hbWorker{key: key, entry: entry, tracker: tracker, done: make(chan struct{})}
}

func (w *hbWorker) start(ctx context.Context) {
	wctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(wctx)
}

// stopped 报告 worker 是否已被停止 (进行中的探测不计入统计)

// stop 停止 worker 并等待进行中的探测退出
func (w *hbWorker) stop() {
	w.mu.Lock()
	w.isStopped = true
	w.mu.Unlock()
	w.cancel()
	w.mu.Lock()
	if w.conn != nil {
		_ = w.conn.Close()
	}
	w.mu.Unlock()
	<-w.done
}

// recordResult 记录探测结果; worker 已停止 (配置变更/下线) 时静默丢弃,
// 防止被强制中断的探测计为真实失败
func (w *hbWorker) recordResult(rtt time.Duration, err error, hb conf.HeartbeatConf) {
	w.mu.Lock()
	stopped := w.isStopped
	w.mu.Unlock()
	if stopped {
		return
	}
	w.tracker.Record(w.key, rtt, err, hb.ManualOnly, hb.Retries)
}

func (w *hbWorker) run(ctx context.Context) {
	defer close(w.done)
	hb := w.entry.Heartbeat
	interval := hb.IntervalDuration()
	for {
		w.probeOnce(hb)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// probeOnce 执行一次探测: 每次新建连接, resolve/dial 失败也计入统计,
// 下一轮 tick 自动重试
func (w *hbWorker) probeOnce(hb conf.HeartbeatConf) {
	raddr, err := net.ResolveUDPAddr("udp", w.key)
	if err != nil {
		w.recordResult(0, fmt.Errorf("resolve entry: %w", err), hb)
		return
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		w.recordResult(0, fmt.Errorf("dial entry: %w", err), hb)
		return
	}
	w.mu.Lock()
	stopped := w.isStopped
	w.conn = conn
	w.mu.Unlock()
	if stopped {
		// stop 已执行, 自行关闭避免泄漏
		_ = conn.Close()
		return
	}
	defer func() {
		w.mu.Lock()
		w.conn = nil
		w.mu.Unlock()
		_ = conn.Close()
	}()

	pkt := make([]byte, packetSize)
	copy(pkt, pingMagic)
	if _, err := rand.Read(pkt[len(pingMagic):]); err != nil {
		logger.Error("heartbeat generate nonce failed", "entry", w.key, "err", err)
		return
	}

	timeout := hb.TimeoutDuration()
	start := time.Now()
	err = conn.SetReadDeadline(start.Add(timeout))
	if err == nil {
		_, err = conn.Write(pkt)
	}
	if err != nil {
		w.recordResult(0, err, hb)
		return
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		w.recordResult(0, err, hb)
		return
	}
	if !isPongFor(buf[:n], pkt) {
		w.recordResult(0, errors.New("invalid pong"), hb)
		return
	}
	w.recordResult(time.Since(start), nil, hb)
}

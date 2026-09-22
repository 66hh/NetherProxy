package multiplexer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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

// EntryStats 线路心跳统计
type EntryStats struct {
	Healthy          bool      `json:"healthy"`
	TotalProbes      uint64    `json:"total_probes"`
	FailedProbes     uint64    `json:"failed_probes"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	LastRTTMs        float64   `json:"last_rtt_ms"`
	LastError        string    `json:"last_error,omitempty"`
	LastChange       time.Time `json:"last_change"`

	unresponsive bool // 已记录过"无响应"日志, 避免刷屏
}

// EntryTracker 跟踪各线路的心跳统计与可用状态, 统计持久化到独立 JSON 文件
type EntryTracker struct {
	mu    sync.RWMutex
	stats map[string]*EntryStats

	path  string // 统计持久化文件路径
	dirty bool   // 有待落盘的变更

	cancel context.CancelFunc
	done   chan struct{}
}

// NewEntryTracker 创建跟踪器, 加载既有统计并启动后台落盘循环
func NewEntryTracker(path string) *EntryTracker {
	t := &EntryTracker{stats: make(map[string]*EntryStats), path: path}
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
		v.unresponsive = false
		t.stats[k] = &v
		if v.Healthy {
			entryHealthy.WithLabelValues(k).Set(1)
		} else {
			entryHealthy.WithLabelValues(k).Set(0)
		}
		entryHeartbeatRTT.WithLabelValues(k).Set(v.LastRTTMs / 1000)
	}
}

// markDirty 标记有待落盘的变更。调用者须持有锁。
func (t *EntryTracker) markDirty() {
	t.dirty = true
}

// flushLoop 周期性将变更落盘 (合并高频更新)
func (t *EntryTracker) flushLoop(ctx context.Context) {
	defer close(t.done)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.flush()
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
		return
	}
	if err := os.Rename(tmp, t.path); err != nil {
		logger.Error("replace entry stats failed", "path", t.path, "err", err)
	}
}

// Healthy 报告线路是否可参与路由 (未知线路默认可用)
func (t *EntryTracker) Healthy(key string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.stats[key]
	return s == nil || s.Healthy
}

// Record 记录一次探测结果。autoOffline 为 true 且连续失败达到 retries 时
// 将线路标记为下线; 否则仅记录统计与日志, 不影响路由。
func (t *EntryTracker) Record(key string, rtt time.Duration, probeErr error, autoOffline bool, retries int) {
	t.mu.Lock()
	s := t.stats[key]
	if s == nil {
		s = &EntryStats{Healthy: true, LastChange: time.Now()}
		t.stats[key] = s
	}
	s.TotalProbes++
	t.markDirty()

	if probeErr != nil {
		s.FailedProbes++
		s.ConsecutiveFails++
		s.LastError = probeErr.Error()
		entryHeartbeatTotal.WithLabelValues(key, "failure").Inc()
		if s.ConsecutiveFails >= retries && !s.unresponsive {
			s.unresponsive = true
			if autoOffline && s.Healthy {
				s.Healthy = false
				s.LastChange = time.Now()
				entryHealthy.WithLabelValues(key).Set(0)
			}
			logger.Error("entry unresponsive", "entry", key,
				"consecutive_fails", s.ConsecutiveFails, "auto_offline", autoOffline, "err", probeErr)
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

// Snapshot 返回全部线路统计的副本
func (t *EntryTracker) Snapshot() map[string]EntryStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]EntryStats, len(t.stats))
	for k, s := range t.stats {
		out[k] = *s
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

// Close 停止全部心跳 worker
func (h *Heartbeat) Close() {
	h.cancel()
	<-h.done
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
	cfg := h.store.Get()
	for i := range cfg.Entry {
		e := &cfg.Entry[i]
		if e.Enable && e.Heartbeat.Enable {
			want[EntryKey(e)] = *e
		}
	}

	h.mu.Lock()
	for key, w := range h.workers {
		e, ok := want[key]
		if !ok {
			w.stop()
			delete(h.workers, key)
			h.tracker.Remove(key)
			logger.Debug("heartbeat worker removed", "entry", key)
			continue
		}
		if w.entry != e {
			w.stop()
			h.workers[key] = newHBWorker(key, e, h.tracker)
			h.workers[key].start(ctx)
			logger.Debug("heartbeat worker restarted", "entry", key)
		}
	}
	for key, e := range want {
		if _, ok := h.workers[key]; !ok {
			w := newHBWorker(key, e, h.tracker)
			h.workers[key] = w
			w.start(ctx)
			logger.Info("heartbeat started", "entry", key,
				"interval", e.Heartbeat.Interval, "auto_offline", e.Heartbeat.AutoOffline)
		}
	}
	h.mu.Unlock()
}

// hbWorker 是单条线路的探测 worker
type hbWorker struct {
	key     string
	entry   conf.EntryConf
	tracker *EntryTracker
	cancel  context.CancelFunc
}

func newHBWorker(key string, entry conf.EntryConf, tracker *EntryTracker) *hbWorker {
	return &hbWorker{key: key, entry: entry, tracker: tracker}
}

func (w *hbWorker) start(ctx context.Context) {
	wctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(wctx)
}

func (w *hbWorker) stop() {
	w.cancel()
}

func (w *hbWorker) run(ctx context.Context) {
	raddr, err := net.ResolveUDPAddr("udp", w.key)
	if err != nil {
		logger.Error("heartbeat resolve entry failed", "entry", w.key, "err", err)
		return
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		logger.Error("heartbeat dial entry failed", "entry", w.key, "err", err)
		return
	}
	defer conn.Close()

	hb := w.entry.Heartbeat
	interval := hb.IntervalDuration()
	timeout := hb.TimeoutDuration()

	w.probe(conn, timeout, hb)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.probe(conn, timeout, hb)
		}
	}
}

// probe 发送一次 NPING 并在超时内等待匹配的 NPONG
func (w *hbWorker) probe(conn *net.UDPConn, timeout time.Duration, hb conf.HeartbeatConf) {
	pkt := make([]byte, packetSize)
	copy(pkt, pingMagic)
	if _, err := rand.Read(pkt[len(pingMagic):]); err != nil {
		logger.Error("heartbeat generate nonce failed", "entry", w.key, "err", err)
		return
	}

	start := time.Now()
	err := conn.SetReadDeadline(start.Add(timeout))
	if err == nil {
		_, err = conn.Write(pkt)
	}
	if err != nil {
		w.tracker.Record(w.key, 0, err, hb.AutoOffline, hb.Retries)
		return
	}

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		w.tracker.Record(w.key, 0, err, hb.AutoOffline, hb.Retries)
		return
	}
	if !isPongFor(buf[:n], pkt) {
		w.tracker.Record(w.key, 0, errors.New("invalid pong"), hb.AutoOffline, hb.Retries)
		return
	}
	w.tracker.Record(w.key, time.Since(start), nil, hb.AutoOffline, hb.Retries)
}

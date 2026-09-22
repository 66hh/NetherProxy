package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
)

var (
	bdsHealthy = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "bds",
		Name:      "healthy",
		Help:      "Whether the BDS backend is currently routable (1) or offline (0).",
	}, []string{"bds"})

	bdsProbeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "bds",
		Name:      "probe_total",
		Help:      "Total number of health probes per BDS backend.",
	}, []string{"bds", "result"})

	bdsProbeRTT = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "bds",
		Name:      "probe_rtt_seconds",
		Help:      "Last successful health probe round-trip time in seconds.",
	}, []string{"bds"})
)

func init() {
	prometheus.MustRegister(bdsHealthy, bdsProbeTotal, bdsProbeRTT)
}

// bdsStatusEvent 一次健康状态变化
type bdsStatusEvent struct {
	Time    time.Time `json:"time"`
	Healthy bool      `json:"healthy"`
}

// bdsStats BDS 健康统计
type bdsStats struct {
	Healthy          bool             `json:"healthy"`
	TotalProbes      uint64           `json:"total_probes"`
	FailedProbes     uint64           `json:"failed_probes"`
	ConsecutiveFails int              `json:"consecutive_fails"`
	LastRTTMs        float64          `json:"last_rtt_ms"`
	LastError        string           `json:"last_error,omitempty"`
	LastChange       time.Time        `json:"last_change"`
	History          []bdsStatusEvent `json:"history,omitempty"` // 状态变化时间线, 用于状态图

	unresponsive bool // 已记录过"无响应"日志, 避免刷屏
}

// bdsTracker 跟踪各 BDS 的健康统计与可用状态
type bdsTracker struct {
	store *conf.Store
	mu    sync.RWMutex
	stats map[string]*bdsStats
}

func newBDSTracker(store *conf.Store) *bdsTracker {
	return &bdsTracker{store: store, stats: make(map[string]*bdsStats)}
}

// bdsKey 返回 BDS 的标识 (host:port)
func bdsKey(b *conf.BDSConf) string {
	return net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
}

// healthy 报告 BDS 是否可用 (未知或无探测记录默认可用)
func (t *bdsTracker) healthy(key string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.stats[key]
	return s == nil || s.Healthy
}

// snapshot 返回全部统计副本
func (t *bdsTracker) snapshot() map[string]bdsStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]bdsStats, len(t.stats))
	for k, s := range t.stats {
		out[k] = *s
	}
	return out
}

// record 记录一次探测结果, 语义与 EntryTracker.Record 一致
func (t *bdsTracker) record(key string, rtt time.Duration, probeErr error, autoOffline bool, retries int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats[key]
	if s == nil {
		s = &bdsStats{Healthy: true, LastChange: time.Now()}
		t.stats[key] = s
	}
	s.TotalProbes++

	if probeErr != nil {
		s.FailedProbes++
		s.ConsecutiveFails++
		s.LastError = probeErr.Error()
		bdsProbeTotal.WithLabelValues(key, "failure").Inc()
		if s.ConsecutiveFails >= retries && !s.unresponsive {
			s.unresponsive = true
			if autoOffline && s.Healthy {
				s.Healthy = false
				s.LastChange = time.Now()
				s.appendHistory(false, t.historySize())
				bdsHealthy.WithLabelValues(key).Set(0)
			}
			logger.Error("bds unresponsive", "bds", key,
				"consecutive_fails", s.ConsecutiveFails, "auto_offline", autoOffline, "err", probeErr)
		}
		return
	}

	s.ConsecutiveFails = 0
	s.LastError = ""
	s.LastRTTMs = float64(rtt.Microseconds()) / 1000
	bdsProbeTotal.WithLabelValues(key, "success").Inc()
	bdsProbeRTT.WithLabelValues(key).Set(rtt.Seconds())
	if s.unresponsive {
		s.unresponsive = false
		logger.Info("bds responsive again", "bds", key, "rtt_ms", s.LastRTTMs)
	}
	if !s.Healthy {
		s.Healthy = true
		s.LastChange = time.Now()
		s.appendHistory(true, t.historySize())
		bdsHealthy.WithLabelValues(key).Set(1)
		logger.Info("bds back online", "bds", key)
	}
}

// appendHistory 追加状态变化事件, 超出上限丢弃最旧。调用者须持有锁。
func (s *bdsStats) appendHistory(healthy bool, maxSize int) {
	if maxSize <= 0 {
		maxSize = 500
	}
	s.History = append(s.History, bdsStatusEvent{Time: time.Now(), Healthy: healthy})
	if len(s.History) > maxSize {
		s.History = s.History[len(s.History)-maxSize:]
	}
}

func (t *bdsTracker) historySize() int {
	return t.store.Get().Stats.StatusHistorySize
}

// remove 移除统计与指标序列 (BDS 从配置中删除时调用)
func (t *bdsTracker) remove(key string) {
	t.mu.Lock()
	delete(t.stats, key)
	t.mu.Unlock()
	bdsHealthy.DeleteLabelValues(key)
	bdsProbeTotal.DeleteLabelValues(key, "success")
	bdsProbeTotal.DeleteLabelValues(key, "failure")
	bdsProbeRTT.DeleteLabelValues(key)
}

// bdsProber 管理全部 BDS 的健康探测 worker (HTTP GET /v1/join), 随配置热更新增删
type bdsProber struct {
	store   *conf.Store
	tracker *bdsTracker
	client  *http.Client

	mu      sync.Mutex
	workers map[string]*bdsWorker
	cancel  context.CancelFunc
	done    chan struct{}
}

func newBDSProber(store *conf.Store, tracker *bdsTracker) *bdsProber {
	return &bdsProber{
		store:   store,
		tracker: tracker,
		client:  &http.Client{},
		workers: make(map[string]*bdsWorker),
	}
}

func (p *bdsProber) start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go p.reconcileLoop(ctx)
}

func (p *bdsProber) close() {
	p.cancel()
	<-p.done
}

func (p *bdsProber) reconcileLoop(ctx context.Context) {
	defer close(p.done)
	p.reconcile(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcile(ctx)
		}
	}
}

func (p *bdsProber) reconcile(ctx context.Context) {
	want := make(map[string]conf.BDSConf)
	exists := make(map[string]bool)
	cfg := p.store.Get()
	for i := range cfg.BDS {
		b := &cfg.BDS[i]
		exists[bdsKey(b)] = true
		if b.Enable && b.Heartbeat.Enable {
			want[bdsKey(b)] = *b
		}
	}

	// 清理已从配置删除的 BDS 统计
	for key := range p.tracker.snapshot() {
		if !exists[key] {
			p.tracker.remove(key)
		}
	}

	p.mu.Lock()
	for key, w := range p.workers {
		b, ok := want[key]
		if !ok {
			w.stop()
			delete(p.workers, key)
			continue
		}
		if w.bds != b {
			w.stop()
			w = newBDSWorker(key, b, p.tracker, p.client)
			p.workers[key] = w
			w.start(ctx)
		}
	}
	for key, b := range want {
		if _, ok := p.workers[key]; !ok {
			w := newBDSWorker(key, b, p.tracker, p.client)
			p.workers[key] = w
			w.start(ctx)
			logger.Info("bds health probe started", "bds", key, "interval", b.Heartbeat.Interval)
		}
	}
	p.mu.Unlock()
}

// bdsWorker 单个 BDS 的探测 worker
type bdsWorker struct {
	key     string
	bds     conf.BDSConf
	tracker *bdsTracker
	client  *http.Client
	cancel  context.CancelFunc
	done    chan struct{}
}

func newBDSWorker(key string, bds conf.BDSConf, tracker *bdsTracker, client *http.Client) *bdsWorker {
	return &bdsWorker{key: key, bds: bds, tracker: tracker, client: client, done: make(chan struct{})}
}

func (w *bdsWorker) start(ctx context.Context) {
	wctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(wctx)
}

func (w *bdsWorker) stop() {
	w.cancel()
	<-w.done
}

func (w *bdsWorker) run(ctx context.Context) {
	defer close(w.done)
	hb := w.bds.Heartbeat
	interval := hb.IntervalDuration()
	for {
		w.probeOnce(ctx, hb)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// probeOnce 执行一次 HTTP GET /v1/join 探测
func (w *bdsWorker) probeOnce(ctx context.Context, hb conf.HeartbeatConf) {
	timeout := hb.TimeoutDuration()
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s/v1/join", w.key)
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, url, nil)
	if err != nil {
		w.tracker.record(w.key, 0, err, hb.AutoOffline, hb.Retries)
		return
	}
	start := time.Now()
	resp, err := w.client.Do(req)
	if err != nil {
		w.tracker.record(w.key, 0, err, hb.AutoOffline, hb.Retries)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.tracker.record(w.key, 0, fmt.Errorf("status %d", resp.StatusCode), hb.AutoOffline, hb.Retries)
		return
	}
	w.tracker.record(w.key, time.Since(start), nil, hb.AutoOffline, hb.Retries)
}

// handleBDSStatus 返回各 BDS 的配置、路由状态与健康统计
func handleBDSStatus(store *conf.Store, tracker *bdsTracker) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats := tracker.snapshot()
		backends := make([]gin.H, 0)
		cfg := store.Get()
		for i := range cfg.BDS {
			b := &cfg.BDS[i]
			key := bdsKey(b)
			entry := gin.H{
				"key":       key,
				"domain":    b.Domain,
				"enable":    b.Enable,
				"heartbeat": b.Heartbeat,
				"healthy":   tracker.healthy(key),
			}
			if s, ok := stats[key]; ok {
				entry["stats"] = s
			}
			backends = append(backends, entry)
		}
		c.JSON(http.StatusOK, gin.H{"bds": backends})
	}
}

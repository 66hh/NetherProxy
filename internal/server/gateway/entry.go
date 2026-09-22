package gateway

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/multiplexer"
	"NetherProxy/internal/session"
)

var entryActiveSessions = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "netherproxy",
	Subsystem: "entry",
	Name:      "active_sessions",
	Help:      "Current number of sessions assigned to the entry line.",
}, []string{"entry"})

func init() {
	prometheus.MustRegister(entryActiveSessions)
}

// entryBalancer 按最少会话数在健康线路间分配新会话, 尊重 max_session 上限;
// 心跳判定下线的线路自动跳过, 恢复后自动参与分配
type entryBalancer struct {
	store   *conf.Store
	tracker *multiplexer.EntryTracker

	mu     sync.Mutex
	counts map[string]int
}

func newEntryBalancer(store *conf.Store, table *session.Table, tracker *multiplexer.EntryTracker) *entryBalancer {
	b := &entryBalancer{store: store, tracker: tracker, counts: make(map[string]int)}
	table.OnRemove = func(s *session.Session) {
		b.release(s.Entry)
	}
	return b
}

// pick 选择当前会话数最少且未满的健康线路, 选中后立即计数; 无可用线路时返回错误
func (b *entryBalancer) pick() (*conf.EntryConf, error) {
	cfg := b.store.Get()
	best, bestN := -1, int(^uint(0)>>1)

	b.mu.Lock()
	for i := range cfg.Entry {
		e := &cfg.Entry[i]
		if !e.Enable {
			continue
		}
		key := multiplexer.EntryKey(e)
		if !b.tracker.Healthy(key) {
			continue
		}
		n := b.counts[key]
		if e.MaxSession > 0 && n >= e.MaxSession {
			continue
		}
		if n < bestN {
			best, bestN = i, n
		}
	}
	if best < 0 {
		b.mu.Unlock()
		return nil, errors.New("no available entry")
	}
	e := &cfg.Entry[best]
	b.counts[multiplexer.EntryKey(e)]++
	b.mu.Unlock()

	b.syncMetric(multiplexer.EntryKey(e))
	logger.Debug("entry session assigned", "entry", multiplexer.EntryKey(e))
	return e, nil
}

// release 会话关闭时释放线路计数
func (b *entryBalancer) release(key string) {
	if key == "" {
		return
	}
	b.mu.Lock()
	if b.counts[key] > 0 {
		b.counts[key]--
	}
	b.mu.Unlock()
	b.syncMetric(key)
}

// syncMetric 将线路计数同步到 Prometheus 指标
func (b *entryBalancer) syncMetric(key string) {
	b.mu.Lock()
	n := b.counts[key]
	b.mu.Unlock()
	entryActiveSessions.WithLabelValues(key).Set(float64(n))
}

// Snapshot 返回各线路活跃会话数副本
func (b *entryBalancer) Snapshot() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.counts))
	for k, v := range b.counts {
		out[k] = v
	}
	return out
}

// resolveEntryIP 将线路主机解析为 IP: 本身是 IP 直接用, 域名走 DNS 解析 (优先 IPv4)
func resolveEntryIP(host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip, nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, ip := range ips {
		if ip.Is4() {
			return ip, nil
		}
	}
	if len(ips) > 0 {
		return ips[0], nil
	}
	return netip.Addr{}, errors.New("no address resolved for entry host")
}

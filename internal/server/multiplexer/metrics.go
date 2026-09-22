package multiplexer

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"NetherProxy/internal/session"
)

var (
	sessionActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "session",
		Name:      "active",
		Help:      "Current number of active sessions.",
	})

	sessionTrafficBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "session",
		Name:      "traffic_bytes",
		Help:      "Traffic bytes per session (rx: client->bds, tx: bds->client).",
	}, []string{"player", "ufrag", "direction"})
)

func init() {
	prometheus.MustRegister(sessionActive, sessionTrafficBytes)
}

// registerSessionMetrics 注册会话指标的同步循环与清理钩子
func (m *Multiplexer) registerSessionMetrics() {
	// 会话移除时清理对应的指标序列
	m.table.AddOnRemove(func(s *session.Session) {
		sessionActive.Dec()
		sessionTrafficBytes.DeleteLabelValues(s.Player, s.Ufrag, "rx")
		sessionTrafficBytes.DeleteLabelValues(s.Player, s.Ufrag, "tx")
	})

	// 定期从会话表同步流量计数到指标
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.table.Range(func(s *session.Session) {
					rx, tx := s.Traffic()
					sessionTrafficBytes.WithLabelValues(s.Player, s.Ufrag, "rx").Set(float64(rx))
					sessionTrafficBytes.WithLabelValues(s.Player, s.Ufrag, "tx").Set(float64(tx))
				})
			}
		}
	}()
}

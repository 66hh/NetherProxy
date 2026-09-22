package multiplexer

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"NetherProxy/internal/session"
)

var (
	// droppedTotal 按原因分类的数据面丢包计数
	droppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "multiplexer",
		Name:      "dropped_total",
		Help:      "Total packets dropped by reason (unknown_ufrag/integrity_failed/unknown_tuple).",
	}, []string{"reason"})

	// trafficBytes 数据面全局流量 (rx: 客户端->bds, tx: bds->客户端)
	trafficBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "multiplexer",
		Name:      "traffic_bytes",
		Help:      "Total traffic bytes forwarded by direction.",
	}, []string{"direction"})

	// sessionByState 各状态的会话数分布
	sessionByState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "netherproxy",
		Subsystem: "session",
		Name:      "by_state",
		Help:      "Current sessions by lifecycle state.",
	}, []string{"state"})

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
	prometheus.MustRegister(sessionActive, sessionTrafficBytes, droppedTotal, trafficBytes, sessionByState)
}

// DropCount 记录一次数据面丢包
func DropCount(reason string) {
	droppedTotal.WithLabelValues(reason).Inc()
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
				byState := make(map[string]int)
				m.table.Range(func(s *session.Session) {
					rx, tx := s.Traffic()
					sessionTrafficBytes.WithLabelValues(s.Player, s.Ufrag, "rx").Set(float64(rx))
					sessionTrafficBytes.WithLabelValues(s.Player, s.Ufrag, "tx").Set(float64(tx))
					byState[s.State().String()]++
				})
				for _, st := range []string{"Signaled", "ICEChecking", "Active", "Idle"} {
					sessionByState.WithLabelValues(st).Set(float64(byState[st]))
				}
			}
		}
	}()
}

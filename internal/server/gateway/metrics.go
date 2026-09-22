package gateway

import (
	"github.com/prometheus/client_golang/prometheus"
)

// 信令层事件指标
var (
	// joinTotal 按结果分类的 join (POST /v1/join/:networkID) 计数
	joinTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "gateway",
		Name:      "join_total",
		Help:      "Total join attempts by result.",
	}, []string{"result"})

	// motdTotal 按结果分类的 MOTD (GET /v1/join) 计数
	motdTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "gateway",
		Name:      "motd_total",
		Help:      "Total MOTD requests by result.",
	}, []string{"result"})
)

func init() {
	prometheus.MustRegister(joinTotal, motdTotal)
}

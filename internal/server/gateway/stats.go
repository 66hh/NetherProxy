package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/server/multiplexer"
	"NetherProxy/internal/session"
)

// StatsPoint 一个采样点的代理状态
type StatsPoint struct {
	Time         time.Time `json:"time"`
	Sessions     int       `json:"sessions"`      // 活跃会话数
	RxRate       float64   `json:"rx_rate"`       // 客户端->BDS 速率 (bytes/s)
	TxRate       float64   `json:"tx_rate"`       // BDS->客户端 速率 (bytes/s)
	EntriesUp    int       `json:"entries_up"`    // 健康线路数
	EntriesTotal int       `json:"entries_total"` // 启用线路数
	BDSUp        int       `json:"bds_up"`        // 健康 BDS 数
	BDSTotal     int       `json:"bds_total"`     // 启用 BDS 数
	TotalRx      uint64    `json:"total_rx"`      // 累计流量
	TotalTx      uint64    `json:"total_tx"`
}

// statsSampler 按固定间隔采样代理状态, 保留最近 N 点供面板折线图
type statsSampler struct {
	mu     sync.RWMutex
	points []StatsPoint

	table   *session.Table
	mux     *multiplexer.Multiplexer
	tracker *multiplexer.EntryTracker
	bdsT    *bdsTracker
	store   *conf.Store

	cancel context.CancelFunc
}

// statsKeep 保留的采样点数 (2s 间隔 ≈ 最近 10 分钟)
const statsKeep = 300

func newStatsSampler(store *conf.Store, table *session.Table, mux *multiplexer.Multiplexer, tracker *multiplexer.EntryTracker, bdsT *bdsTracker) *statsSampler {
	s := &statsSampler{table: table, mux: mux, tracker: tracker, bdsT: bdsT, store: store}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.loop(ctx)
	return s
}

func (s *statsSampler) close() { s.cancel() }

func (s *statsSampler) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastRx, lastTx uint64
	lastTime := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			rx, tx := s.mux.TotalTraffic()
			dt := now.Sub(lastTime).Seconds()
			p := StatsPoint{Time: now, TotalRx: rx, TotalTx: tx}
			if dt > 0 {
				p.RxRate = float64(rx-lastRx) / dt
				p.TxRate = float64(tx-lastTx) / dt
			}
			lastRx, lastTx, lastTime = rx, tx, now

			p.Sessions = s.table.Len()
			cfg := s.store.Get()
			for i := range cfg.Entry {
				if !cfg.Entry[i].Enable {
					continue
				}
				p.EntriesTotal++
				if s.tracker.Healthy(multiplexer.EntryKey(&cfg.Entry[i])) {
					p.EntriesUp++
				}
			}
			for i := range cfg.BDS {
				if !cfg.BDS[i].Enable {
					continue
				}
				p.BDSTotal++
				if s.bdsT.healthy(bdsKey(&cfg.BDS[i])) {
					p.BDSUp++
				}
			}

			s.mu.Lock()
			s.points = append(s.points, p)
			if len(s.points) > statsKeep {
				s.points = s.points[len(s.points)-statsKeep:]
			}
			s.mu.Unlock()
		}
	}
}

// handleStats 返回采样点序列 (面板折线图数据源)
func (s *statsSampler) handleStats(c *gin.Context) {
	s.mu.RLock()
	points := make([]StatsPoint, len(s.points))
	copy(points, s.points)
	s.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"points": points})
}

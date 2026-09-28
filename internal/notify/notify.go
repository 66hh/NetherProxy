// Package notify 健康状态告警通知: 线路/BDS 无响应 (及恢复) 时
// 异步 POST JSON 到配置的 webhook。配置每次发送时读取, 热更即时生效。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
)

// 事件类型
const (
	EntryDown = "entry_down" // 线路无响应 (连续失败达到阈值)
	EntryUp   = "entry_up"   // 线路恢复
	BDSDown   = "bds_down"   // BDS 无响应
	BDSUp     = "bds_up"     // BDS 恢复
)

// Event 告警事件 payload
type Event struct {
	Event            string    `json:"event"` // entry_down/entry_up/bds_down/bds_up
	Key              string    `json:"key"`   // 线路/BDS 标识 (host:port)
	Time             time.Time `json:"time"`
	ConsecutiveFails int       `json:"consecutive_fails,omitempty"`
	Error            string    `json:"error,omitempty"`
}

type Notifier struct {
	store  *conf.Store
	client *http.Client
}

func New(store *conf.Store) *Notifier {
	return &Notifier{store: store, client: &http.Client{}}
}

// Send 发送告警 (异步, 不阻塞调用方)。未启用时直接返回;
// 恢复事件 (entry_up/bds_up) 需要 notify.on_recovery 显式开启。
func (n *Notifier) Send(event, key string, fails int, errStr string) {
	cfg := n.store.Get().Notify
	if !cfg.Enable || cfg.URL == "" {
		return
	}
	if (event == EntryUp || event == BDSUp) && !cfg.OnRecovery {
		return
	}
	ev := Event{Event: event, Key: key, Time: time.Now(), ConsecutiveFails: fails, Error: errStr}
	go n.post(cfg, ev)
}

func (n *Notifier) post(cfg conf.NotifyConf, ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		return // Event 结构固定, 实际不可能失败
	}
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		logger.Error("notify build request failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		logger.Error("notify webhook failed", "event", ev.Event, "key", ev.Key, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		logger.Warn("notify webhook returned non-2xx", "event", ev.Event, "key", ev.Key, "status", resp.StatusCode)
		return
	}
	logger.Info("notify webhook sent", "event", ev.Event, "key", ev.Key)
}

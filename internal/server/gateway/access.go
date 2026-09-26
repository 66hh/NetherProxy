package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
)

// checkAccess 按配置执行 XUID 黑白名单与 webhook 判定, 返回是否放行与拒绝原因
// 配置每次请求从 Store 读取, 热更即时生效
func (h *joinHandler) checkAccess(player PlayerInfo, clientIP string) (bool, string) {
	cfg := h.store.Get().Gateway.Access

	// 静态名单
	listed := slices.Contains(cfg.XUIDs, player.XUID)
	switch cfg.Mode {
	case "blacklist":
		if listed {
			return false, "xuid in blacklist"
		}
	case "whitelist":
		if !listed {
			return false, "xuid not in whitelist"
		}
	}

	// webhook 动态判定
	if cfg.Webhook.Enable {
		if err := h.callWebhook(cfg.Webhook, player, clientIP); err != nil {
			// webhook 调用失败一律拒绝 (fail closed), 避免旁路名单
			return false, "webhook: " + err.Error()
		}
	}
	return true, ""
}

// callWebhook 调用外部服务判定是否放行; 返回 nil 表示放行
func (h *joinHandler) callWebhook(cfg conf.WebhookConf, player PlayerInfo, clientIP string) error {
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	reqBody, err := json.Marshal(map[string]string{
		"xuid":      player.XUID,
		"xname":     player.Name,
		"client_ip": clientIP,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	var result struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.Allow {
		if result.Reason != "" {
			return fmt.Errorf("denied: %s", result.Reason)
		}
		return fmt.Errorf("denied")
	}
	return nil
}

// rateLimiter 按 XUID 的固定窗口 join 频率限制器
type rateLimiter struct {
	mu   sync.Mutex
	hits map[string]*rateHit
}

type rateHit struct {
	windowStart time.Time
	count       int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{hits: make(map[string]*rateHit)}
}

// allow 判定该 key 本次 join 是否未超限额, 窗口与上限每次从配置读取
func (r *rateLimiter) allow(key string, cfg conf.RateLimitConf) bool {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil || interval <= 0 {
		interval = time.Minute
	}
	maxJoins := max(cfg.MaxJoins, 1)
	maxKeys := cfg.MaxKeys
	if maxKeys <= 0 {
		maxKeys = 1000
	}

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.hits) >= maxKeys {
		if _, exists := r.hits[key]; !exists {
			// 容量满且是新 key: 拒绝, 防止伪造 key 冲掉全部现有窗口
			logger.Warn("rate limiter full, rejecting new key", "key", key, "max_keys", maxKeys)
			return false
		}
	}

	h := r.hits[key]
	if h == nil || now.Sub(h.windowStart) >= interval {
		r.hits[key] = &rateHit{windowStart: now, count: 1}
		return true
	}
	if h.count >= maxJoins {
		logger.Debug("join rate limited", "key", key, "count", h.count, "window", interval)
		return false
	}
	h.count++
	return true
}

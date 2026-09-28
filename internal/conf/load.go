package conf

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// randomHex 生成 n 字节的随机十六进制字符串
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("generate random token: %w", err))
	}
	return hex.EncodeToString(b)
}

// Default 返回默认配置
func Default() *Conf {
	return &Conf{
		Log: LogConf{
			Level:      "info",
			Format:     "text",
			MaxSize:    100,
			MaxBackups: 7,
			MaxAge:     30,
			BufferSize: 1000,
		},
		Gateway: GatewayConf{
			Host:           "0.0.0.0",
			Port:           19130,
			Token:          randomHex(16),
			VerifyIdentity: true,
			MotdCache:      "0s",
			APIAuthExempt:  []string{"/api/healthz"},
			Access: AccessConf{
				Mode:  "off",
				XUIDs: []string{},
				Webhook: WebhookConf{
					Timeout: "3s",
				},
			},
			RateLimit: RateLimitConf{
				Interval: "60s",
				MaxJoins: 5,
				MaxKeys:  1000,
			},
		},
		Multiplexer: MultiplexerConf{
			Host: "0.0.0.0",
			Port: 19131,
		},
		Stats: StatsConf{
			StatusHistorySize: 500,
			FlushInterval:     "30s",
		},
		Notify: NotifyConf{
			Timeout: "5s",
		},
		Session: SessionConf{
			SignaledTimeout:   "30s",
			ActiveIdleTimeout: "120s",
			IdleReapTimeout:   "300s",
			TupleStaleTimeout: "60s",
		},
		BDS: []BDSConf{
			{
				Enable: true,
				Domain: "*",
				Host:   "127.0.0.1",
				Port:   19132,
				Heartbeat: HeartbeatConf{
					Interval: "5s",
					Timeout:  "2s",
					Retries:  3,
				},
			},
		},
		Entry: []EntryConf{
			{
				Enable:     true,
				Host:       "127.0.0.1",
				Port:       19131,
				MaxSession: 100,
				Heartbeat: HeartbeatConf{
					Interval: "5s",
					Timeout:  "2s",
					Retries:  3,
				},
			},
		},
	}
}

// Load 从 YAML 文件加载配置, 未填写的字段使用默认值, 加载后执行校验
func Load(path string) (*Conf, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	normalize(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

// normalize 为数组条目填充缺省字段: yaml 数组整体替换默认数组,
// 条目内未写的子字段需要单独补默认值; 同时规范化全部主机字段
func normalize(c *Conf) {
	for i := range c.BDS {
		c.BDS[i].Host = NormalizeHost(c.BDS[i].Host)
		fillHeartbeatDefaults(&c.BDS[i].Heartbeat)
	}
	for i := range c.Entry {
		c.Entry[i].Host = NormalizeHost(c.Entry[i].Host)
		fillHeartbeatDefaults(&c.Entry[i].Heartbeat)
	}
	c.Gateway.Host = NormalizeHost(c.Gateway.Host)
	c.Multiplexer.Host = NormalizeHost(c.Multiplexer.Host)
	// 其他标量缺省
	if c.Gateway.MotdCache == "" {
		c.Gateway.MotdCache = "0s"
	}
	if c.Stats.FlushInterval == "" {
		c.Stats.FlushInterval = "30s"
	}
	if c.Notify.Timeout == "" {
		c.Notify.Timeout = "5s"
	}
}

// NormalizeHost 规范化主机字段: trim、剥 IPv6 方括号 ([::1] -> ::1)、
// IP 统一为规范表示 (含 4-in-6 Unmap)、域名小写。
// 保证配置校验的去重键与运行时 EntryKey/监听地址一致,
// 带括号或大小写混写的同一地址不会被判为两条线路。
func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return strings.ToLower(host)
}

// fillHeartbeatDefaults 填充心跳配置的缺省值
func fillHeartbeatDefaults(hb *HeartbeatConf) {
	if hb.Interval == "" {
		hb.Interval = "5s"
	}
	if hb.Timeout == "" {
		hb.Timeout = "2s"
	}
	if hb.Retries == 0 {
		hb.Retries = 3
	}
}

// Save 将配置以 YAML 格式写入文件: 先写临时文件再原子替换,
// 防止崩溃留下截断的配置; 文件权限 0600 (含 token 等凭据)
func Save(path string, cfg *Conf) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

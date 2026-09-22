package conf

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

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
		},
		Gateway: GatewayConf{
			Host:           "0.0.0.0",
			Port:           19130,
			Token:          randomHex(16),
			VerifyIdentity: true,
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
					Enable:      false,
					AutoOffline: true,
					Interval:    "5s",
					Timeout:     "2s",
					Retries:     3,
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
					Enable:      false,
					AutoOffline: true,
					Interval:    "5s",
					Timeout:     "2s",
					Retries:     3,
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
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

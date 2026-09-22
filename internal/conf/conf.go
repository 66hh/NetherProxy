package conf

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// TLS配置
type TLSConf struct {
	Enable bool   `yaml:"enable" json:"enable"` // 是否启用TLS
	Cert   string `yaml:"cert" json:"cert"`     // PEM 路径
	Key    string `yaml:"key" json:"key"`       // 私钥路径
}

// 指标配置
//
// Prometheus 抓取配置示例:
//
//	scrape_configs:
//	  - job_name: netherproxy
//	    scheme: https                   # 网关启用 TLS 时
//	    metrics_path: /api/metrics      # 非默认路径时必须指定
//	    authorization:                  # 默认需要认证, 或将 /api/metrics 加入 gateway.api_auth_exempt
//	      type: Bearer
//	      credentials: <gateway.token>  # 也可改用 credentials_file 引用单独文件
//	    tls_config:
//	      insecure_skip_verify: true    # 自签名证书时需要
//	    static_configs:
//	      - targets: ["<gateway.host:gateway.port>"]
type MetricsConf struct {
	Enable bool `yaml:"enable" json:"enable"` // 是否启用 Prometheus 指标 (固定路径 /api/metrics)
}

// 访问控制配置: XUID 黑白名单 + webhook 动态判定
type AccessConf struct {
	Mode    string      `yaml:"mode" json:"mode"`       // off/blacklist/whitelist
	XUIDs   []string    `yaml:"xuids" json:"xuids"`     // XUID 名单
	Webhook WebhookConf `yaml:"webhook" json:"webhook"` // webhook 动态判定
}

// webhook 动态判定配置: join 时向外部服务查询是否放行
type WebhookConf struct {
	Enable  bool   `yaml:"enable" json:"enable"`   // 是否启用
	URL     string `yaml:"url" json:"url"`         // 判定接口, POST {"xuid","xname","client_ip"} 返回 {"allow":bool}
	Timeout string `yaml:"timeout" json:"timeout"` // 调用超时, 如 "3s"
}

// join 频率限制配置: 按 XUID 滑动窗口限流
type RateLimitConf struct {
	Enable   bool   `yaml:"enable" json:"enable"`       // 是否启用
	Interval string `yaml:"interval" json:"interval"`   // 窗口长度, 如 "60s"
	MaxJoins int    `yaml:"max_joins" json:"max_joins"` // 每窗口每 XUID 最大 join 次数
	MaxKeys  int    `yaml:"max_keys" json:"max_keys"`   // 跟踪的最大 key 数, 超过整体重置 (防伪造 XUID 撑大内存), 0 用默认 1000
}

// 网关服务器配置
type GatewayConf struct {
	Host           string        `yaml:"host" json:"host"`                       // 网关主机
	Port           int           `yaml:"port" json:"port"`                       // 网关端口
	Token          string        `yaml:"token" json:"token"`                     // 访问令牌, 默认随机生成
	VerifyIdentity bool          `yaml:"verify_identity" json:"verify_identity"` // 是否验证玩家身份 JWT
	APIAuthExempt  []string      `yaml:"api_auth_exempt" json:"api_auth_exempt"` // 免认证的 API 路由 (如 ["/api/healthz"]), 不在列表中的一律需要 Bearer 认证
	RelayOnly      bool          `yaml:"relay_only" json:"relay_only"`           // 中继模式: 隐藏客户端真实地址 (offer candidate 替换为不可达占位), 强制全部流量经代理
	TLS            TLSConf       `yaml:"tls" json:"tls"`                         // TLS配置
	Metrics        MetricsConf   `yaml:"metrics" json:"metrics"`                 // 指标配置
	Access         AccessConf    `yaml:"access" json:"access"`                   // 访问控制 (黑白名单/webhook)
	RateLimit      RateLimitConf `yaml:"rate_limit" json:"rate_limit"`           // join 频率限制
}

// 端口复用器配置
type MultiplexerConf struct {
	Host string `yaml:"host" json:"host"` // 复用器主机
	Port int    `yaml:"port" json:"port"` // 复用器端口
}

// 会话超时配置
type SessionConf struct {
	SignaledTimeout   string `yaml:"signaled_timeout" json:"signaled_timeout"`       // 信令完成后等待首个 STUN 的超时, 如 "30s"
	ActiveIdleTimeout string `yaml:"active_idle_timeout" json:"active_idle_timeout"` // 活跃会话空闲多久转 Idle, 如 "120s"
	IdleReapTimeout   string `yaml:"idle_reap_timeout" json:"idle_reap_timeout"`     // Idle 会话多久后回收, 如 "300s"
	TupleStaleTimeout string `yaml:"tuple_stale_timeout" json:"tuple_stale_timeout"` // 客户端地址软状态有效期, 如 "60s"
}

// Signaled 信令超时 (默认 30s)
func (s SessionConf) Signaled() time.Duration {
	if d, err := time.ParseDuration(s.SignaledTimeout); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

// ActiveIdle 活跃空闲超时 (默认 120s)
func (s SessionConf) ActiveIdle() time.Duration {
	if d, err := time.ParseDuration(s.ActiveIdleTimeout); err == nil && d > 0 {
		return d
	}
	return 120 * time.Second
}

// IdleReap Idle 回收超时 (默认 300s)
func (s SessionConf) IdleReap() time.Duration {
	if d, err := time.ParseDuration(s.IdleReapTimeout); err == nil && d > 0 {
		return d
	}
	return 300 * time.Second
}

// TupleStale 5-tuple 软状态有效期 (默认 60s)
func (s SessionConf) TupleStale() time.Duration {
	if d, err := time.ParseDuration(s.TupleStaleTimeout); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

// BDS服务器配置, 支持填写多个服务器并绑定域名
type BDSConf struct {
	Enable    bool          `yaml:"enable" json:"enable"`       // 是否启用
	Domain    string        `yaml:"domain" json:"domain"`       // 绑定域名/Ip (支持通配符, 越靠前的条目匹配优先级越高)
	Host      string        `yaml:"host" json:"host"`           // BDS主机
	Port      int           `yaml:"port" json:"port"`           // BDS网关端口
	Heartbeat HeartbeatConf `yaml:"heartbeat" json:"heartbeat"` // 健康探测配置 (HTTP GET /v1/join)
}

// MatchDomain 判断 host 是否匹配配置的 Domain 模式, 匹配不区分大小写:
//   - "*" 匹配任意主机
//   - "*.example.com" 匹配该域名的各级子域名 (不匹配裸域 example.com)
//   - 其余按精确匹配
func (b BDSConf) MatchDomain(host string) bool {
	pattern := strings.ToLower(strings.TrimSpace(b.Domain))
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:] // ".example.com"
		return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
	default:
		return pattern == host
	}
}

// 线路心跳配置, 代理主动探测线路可达性并记录统计
type HeartbeatConf struct {
	Enable      bool   `yaml:"enable" json:"enable"`             // 是否启用心跳探测
	AutoOffline bool   `yaml:"auto_offline" json:"auto_offline"` // 连续失败是否自动下线线路 (false 时仅记录统计与日志)
	Interval    string `yaml:"interval" json:"interval"`         // 心跳间隔, 如 "5s"
	Timeout     string `yaml:"timeout" json:"timeout"`           // 单次响应超时, 如 "2s"
	Retries     int    `yaml:"retries" json:"retries"`           // 连续失败多少次后判定无响应
}

// IntervalDuration 解析心跳间隔
func (h HeartbeatConf) IntervalDuration() time.Duration {
	d, _ := time.ParseDuration(h.Interval)
	return d
}

// TimeoutDuration 解析单次响应超时
func (h HeartbeatConf) TimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(h.Timeout)
	return d
}

// 公网线路配置 (客户端实际连接的地址需要映射到复用器上), 支持填写多条线路网关将会自动平均
type EntryConf struct {
	Enable     bool          `yaml:"enable" json:"enable"`           // 是否启用
	Host       string        `yaml:"host" json:"host"`               // 公网线路主机
	Port       int           `yaml:"port" json:"port"`               // 公网线路端口
	MaxSession int           `yaml:"max_session" json:"max_session"` // 最大会话数, 0 表示不限制
	Heartbeat  HeartbeatConf `yaml:"heartbeat" json:"heartbeat"`     // 心跳探测配置
}

// 日志配置
type LogConf struct {
	Level      string `yaml:"level" json:"level"`             // 日志级别: debug/info/warn/error
	Format     string `yaml:"format" json:"format"`           // 输出格式: text/json
	File       string `yaml:"file" json:"file"`               // 日志文件路径, 为空则仅输出到控制台
	MaxSize    int    `yaml:"max_size" json:"max_size"`       // 单个日志文件最大大小(MB), 超过后轮转
	MaxBackups int    `yaml:"max_backups" json:"max_backups"` // 保留的旧日志文件数量上限, 0 表示不限制
	MaxAge     int    `yaml:"max_age" json:"max_age"`         // 旧日志文件保留天数, 0 表示不限制
	Compress   bool   `yaml:"compress" json:"compress"`       // 是否压缩轮转后的旧日志
}

// 统计配置
type StatsConf struct {
	StatusHistorySize int `yaml:"status_history_size" json:"status_history_size"` // 每条线路/BDS 保留的状态变化历史上限, 0 用默认 500
}

type Conf struct {
	Log         LogConf         `yaml:"log" json:"log"`
	Gateway     GatewayConf     `yaml:"gateway" json:"gateway"`
	Multiplexer MultiplexerConf `yaml:"multiplexer" json:"multiplexer"`
	Session     SessionConf     `yaml:"session" json:"session"`
	Stats       StatsConf       `yaml:"stats" json:"stats"`
	BDS         []BDSConf       `yaml:"bds" json:"bds"`
	Entry       []EntryConf     `yaml:"entry" json:"entry"`
}

// Validate 校验配置合法性, 返回全部校验错误的聚合
func (c *Conf) Validate() error {

	var errs []error

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level: invalid level %q, expect debug/info/warn/error", c.Log.Level))
	}

	switch c.Log.Format {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("log.format: invalid format %q, expect text/json", c.Log.Format))
	}

	if c.Log.MaxSize < 0 {
		errs = append(errs, fmt.Errorf("log.max_size: must not be negative, got %d", c.Log.MaxSize))
	}

	if c.Log.MaxBackups < 0 {
		errs = append(errs, fmt.Errorf("log.max_backups: must not be negative, got %d", c.Log.MaxBackups))
	}

	if c.Log.MaxAge < 0 {
		errs = append(errs, fmt.Errorf("log.max_age: must not be negative, got %d", c.Log.MaxAge))
	}

	if err := checkPort("gateway.port", c.Gateway.Port); err != nil {
		errs = append(errs, err)
	}

	if c.Gateway.TLS.Enable {

		if c.Gateway.TLS.Cert == "" {
			errs = append(errs, errors.New("gateway.tls.cert: required when tls is enabled"))
		}

		if c.Gateway.TLS.Key == "" {
			errs = append(errs, errors.New("gateway.tls.key: required when tls is enabled"))
		}
	}

	// token 是全部需认证 API 的凭据, 不允许为空
	// (空 token 会使 "Bearer " 空校验通过造成认证绕过)
	if c.Gateway.Token == "" {
		errs = append(errs, errors.New("gateway.token: must not be empty"))
	}

	switch c.Gateway.Access.Mode {
	case "", "off", "blacklist", "whitelist":
	default:
		errs = append(errs, fmt.Errorf("gateway.access.mode: invalid mode %q, expect off/blacklist/whitelist", c.Gateway.Access.Mode))
	}
	if c.Gateway.Access.Mode == "whitelist" && len(c.Gateway.Access.XUIDs) == 0 {
		errs = append(errs, errors.New("gateway.access.xuids: whitelist mode with empty list rejects all players"))
	}
	if c.Gateway.Access.Webhook.Enable {
		if c.Gateway.Access.Webhook.URL == "" {
			errs = append(errs, errors.New("gateway.access.webhook.url: required when webhook is enabled"))
		}
		if d, err := time.ParseDuration(c.Gateway.Access.Webhook.Timeout); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("gateway.access.webhook.timeout: invalid duration %q", c.Gateway.Access.Webhook.Timeout))
		}
	}
	if c.Gateway.RateLimit.Enable {
		if d, err := time.ParseDuration(c.Gateway.RateLimit.Interval); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("gateway.rate_limit.interval: invalid duration %q", c.Gateway.RateLimit.Interval))
		}
		if c.Gateway.RateLimit.MaxJoins < 1 {
			errs = append(errs, errors.New("gateway.rate_limit.max_joins: must be >= 1"))
		}
		if c.Gateway.RateLimit.MaxKeys < 0 {
			errs = append(errs, errors.New("gateway.rate_limit.max_keys: must not be negative"))
		}
	}

	if err := checkPort("multiplexer.port", c.Multiplexer.Port); err != nil {
		errs = append(errs, err)
	}

	for _, item := range []struct {
		field string
		value string
	}{
		{"session.signaled_timeout", c.Session.SignaledTimeout},
		{"session.active_idle_timeout", c.Session.ActiveIdleTimeout},
		{"session.idle_reap_timeout", c.Session.IdleReapTimeout},
		{"session.tuple_stale_timeout", c.Session.TupleStaleTimeout},
	} {
		if d, err := time.ParseDuration(item.value); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid duration %q", item.field, item.value))
		}
	}

	seenDomains := make(map[string]bool)
	for i, bds := range c.BDS {

		if !bds.Enable {
			continue
		}

		domain := strings.ToLower(strings.TrimSpace(bds.Domain))
		if domain != "" && seenDomains[domain] {
			errs = append(errs, fmt.Errorf("bds[%d].domain: duplicate domain %q, only the first entry takes effect", i, bds.Domain))
		}
		seenDomains[domain] = true

		if bds.Domain == "" {
			errs = append(errs, fmt.Errorf("bds[%d].domain: required when enabled", i))
		}

		if bds.Host == "" {
			errs = append(errs, fmt.Errorf("bds[%d].host: required when enabled", i))
		}

		if err := checkPort(fmt.Sprintf("bds[%d].port", i), bds.Port); err != nil {
			errs = append(errs, err)
		}

		if err := checkHeartbeat(fmt.Sprintf("bds[%d].heartbeat", i), bds.Heartbeat); err != nil {
			errs = append(errs, err)
		}
	}

	seenEntries := make(map[string]bool)
	for i, entry := range c.Entry {

		if !entry.Enable {
			continue
		}

		entryKey := net.JoinHostPort(entry.Host, strconv.Itoa(entry.Port))
		if seenEntries[entryKey] {
			errs = append(errs, fmt.Errorf("entry[%d]: duplicate entry %s, heartbeat stats and session counts would collide", i, entryKey))
		}
		seenEntries[entryKey] = true

		if entry.Host == "" {
			errs = append(errs, fmt.Errorf("entry[%d].host: required when enabled", i))
		}

		if err := checkPort(fmt.Sprintf("entry[%d].port", i), entry.Port); err != nil {
			errs = append(errs, err)
		}

		if entry.MaxSession < 0 {
			errs = append(errs, fmt.Errorf("entry[%d].max_session: must not be negative", i))
		}

		if err := checkHeartbeat(fmt.Sprintf("entry[%d].heartbeat", i), entry.Heartbeat); err != nil {
			errs = append(errs, err)
		}
	}

	if c.Stats.StatusHistorySize < 0 {
		errs = append(errs, errors.New("stats.status_history_size: must not be negative"))
	}

	return errors.Join(errs...)
}

// checkHeartbeat 校验心跳配置 (未启用时跳过)
func checkHeartbeat(field string, hb HeartbeatConf) error {
	if !hb.Enable {
		return nil
	}
	if _, err := time.ParseDuration(hb.Interval); err != nil || hb.IntervalDuration() <= 0 {
		return fmt.Errorf("%s.interval: invalid duration %q", field, hb.Interval)
	}
	if _, err := time.ParseDuration(hb.Timeout); err != nil || hb.TimeoutDuration() <= 0 {
		return fmt.Errorf("%s.timeout: invalid duration %q", field, hb.Timeout)
	}
	if hb.Retries < 1 {
		return fmt.Errorf("%s.retries: must be >= 1", field)
	}
	return nil
}

func checkPort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: invalid port %d, expect 1-65535", field, port)
	}
	return nil
}

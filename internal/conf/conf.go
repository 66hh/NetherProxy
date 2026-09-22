package conf

import (
	"errors"
	"fmt"
)

// TLS配置
type TLSConf struct {
	Enable bool   `yaml:"enable"` // 是否启用TLS
	Cert   string `yaml:"cert"`   // PEM 路径
	Key    string `yaml:"key"`    // 私钥路径
}

// 指标配置
//
// Prometheus 抓取配置示例:
//
//	scrape_configs:
//	  - job_name: netherproxy
//	    scheme: https                   # 网关启用 TLS 时
//	    metrics_path: /api/metrics      # 非默认路径时必须指定
//	    authorization:                  # auth 开启时配置, 否则省略
//	      type: Bearer
//	      credentials: <gateway.token>  # 也可改用 credentials_file 引用单独文件
//	    tls_config:
//	      insecure_skip_verify: true    # 自签名证书时需要
//	    static_configs:
//	      - targets: ["<gateway.host:gateway.port>"]
type MetricsConf struct {
	Enable bool `yaml:"enable"` // 是否启用 Prometheus 指标 (固定路径 /api/metrics)
	Auth   bool `yaml:"auth"`   // 是否对指标路由启用 token 认证 (通过 Authorization: Bearer <token> 传递)
}

// 网关服务器配置
type GatewayConf struct {
	Host    string      `yaml:"host"`    // 网关主机
	Port    int         `yaml:"port"`    // 网关端口
	Token   string      `yaml:"token"`   // 访问令牌, 默认随机生成
	TLS     TLSConf     `yaml:"tls"`     // TLS配置
	Metrics MetricsConf `yaml:"metrics"` // 指标配置
}

// 端口复用器配置
type MultiplexerConf struct {
	Host string `yaml:"host"` // 复用器主机
	Port int    `yaml:"port"` // 复用器端口
}

// BDS服务器配置, 支持填写多个服务器并绑定域名
type BDSConf struct {
	Enable bool   `yaml:"enable"` // 是否启用
	Domain string `yaml:"domain"` // 绑定域名/Ip (支持通配符, 越靠前的条目匹配优先级越高)
	Host   string `yaml:"host"`   // BDS主机
	Port   int    `yaml:"port"`   // BDS网关端口
}

// 公网线路配置 (客户端实际连接的地址需要映射到复用器上), 支持填写多条线路网关将会自动平均
type EntryConf struct {
	Enable     bool   `yaml:"enable"`      // 是否启用
	Host       string `yaml:"host"`        // 公网线路主机
	Port       int    `yaml:"port"`        // 公网线路端口
	MaxSession int    `yaml:"max_session"` // 最大会话数
}

// 日志配置
type LogConf struct {
	Level      string `yaml:"level"`       // 日志级别: debug/info/warn/error
	Format     string `yaml:"format"`      // 输出格式: text/json
	File       string `yaml:"file"`        // 日志文件路径, 为空则仅输出到控制台
	MaxSize    int    `yaml:"max_size"`    // 单个日志文件最大大小(MB), 超过后轮转
	MaxBackups int    `yaml:"max_backups"` // 保留的旧日志文件数量上限, 0 表示不限制
	MaxAge     int    `yaml:"max_age"`     // 旧日志文件保留天数, 0 表示不限制
	Compress   bool   `yaml:"compress"`    // 是否压缩轮转后的旧日志
}

type Conf struct {
	Log         LogConf         `yaml:"log"`
	Gateway     GatewayConf     `yaml:"gateway"`
	Multiplexer MultiplexerConf `yaml:"multiplexer"`
	BDS         []BDSConf       `yaml:"bds"`
	Entry       []EntryConf     `yaml:"entry"`
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

	if c.Gateway.Metrics.Enable && c.Gateway.Metrics.Auth && c.Gateway.Token == "" {
		errs = append(errs, errors.New("gateway.token: required when metrics auth is enabled"))
	}

	if err := checkPort("multiplexer.port", c.Multiplexer.Port); err != nil {
		errs = append(errs, err)
	}

	for i, bds := range c.BDS {

		if !bds.Enable {
			continue
		}

		if bds.Domain == "" {
			errs = append(errs, fmt.Errorf("bds[%d].domain: required when enabled", i))
		}

		if bds.Host == "" {
			errs = append(errs, fmt.Errorf("bds[%d].host: required when enabled", i))
		}

		if err := checkPort(fmt.Sprintf("bds[%d].port", i), bds.Port); err != nil {
			errs = append(errs, err)
		}
	}

	for i, entry := range c.Entry {

		if !entry.Enable {
			continue
		}

		if entry.Host == "" {
			errs = append(errs, fmt.Errorf("entry[%d].host: required when enabled", i))
		}

		if err := checkPort(fmt.Sprintf("entry[%d].port", i), entry.Port); err != nil {
			errs = append(errs, err)
		}

		if entry.MaxSession < 0 {
			errs = append(errs, fmt.Errorf("entry[%d].max_session: must not be negative", i))
		}
	}

	return errors.Join(errs...)
}

func checkPort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: invalid port %d, expect 1-65535", field, port)
	}
	return nil
}

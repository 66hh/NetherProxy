package conf

// TLS配置
type TLSConf struct {
	Enable bool   `yaml:"enable"` // 是否启用TLS
	Cert   string `yaml:"cert"`   // PEM 路径
	Key    string `yaml:"key"`    // 私钥路径
}

// 网关服务器配置
type GatewayConf struct {
	Host string  `yaml:"host"` // 网关主机
	Port int     `yaml:"port"` // 网关端口
	TLS  TLSConf `yaml:"tls"`  // TLS配置
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

type Conf struct {
	Gateway     GatewayConf     `yaml:"gateway"`
	Multiplexer MultiplexerConf `yaml:"multiplexer"`
	BDS         []BDSConf       `yaml:"bds"`
	Entry       []EntryConf     `yaml:"entry"`
}

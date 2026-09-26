// Package gateway 网关 HTTP 服务
package gateway

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/multiplexer"
)

//go:embed web/index.html
var panelHTML []byte

var (
	requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "netherproxy",
		Subsystem: "gateway",
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests handled by the gateway.",
	}, []string{"method", "path", "status"})

	requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "netherproxy",
		Subsystem: "gateway",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})
)

func init() {
	prometheus.MustRegister(requestsTotal, requestDuration)
	// 关闭 gin 自身的调试输出, 统一走项目日志
	gin.SetMode(gin.ReleaseMode)
}

// Gateway 网关 HTTP 服务
type Gateway struct {
	store  *conf.Store
	srv    *http.Server
	prober *bdsProber
	stats  *statsSampler
	dualLn atomic.Pointer[net.Listener] // dual 模式的底层 TCP listener (Shutdown 时显式关闭)
}

// New 创建网关服务, 监听地址/TLS/metrics 开关取启动时配置, 之后重载不生效
func New(store *conf.Store, mux *multiplexer.Multiplexer, tracker *multiplexer.EntryTracker) *Gateway {
	cfg := store.Get().Gateway
	router := gin.New()
	// 不信任任何代理头 (X-Forwarded-For), ClientIP 直接取对端地址
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.Recovery(), accessLog())
	if cfg.Metrics.Enable {
		router.Use(collectMetrics())
	}

	// 控制面板
	router.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", panelHTML)
	})

	// NetherNet 信令端点, 客户端硬编码路径, 必须挂在根路径
	balancer := newEntryBalancer(store, mux.Table(), tracker)
	bdsTrack := newBDSTracker(store)
	prober := newBDSProber(store, bdsTrack)
	prober.start()
	stats := newStatsSampler(store, mux.Table(), mux, tracker, bdsTrack)
	join := newJoinHandler(store, balancer, mux, bdsTrack)
	router.GET("/v1/join", join.motd)
	router.POST("/v1/join/:networkID", join.offer)

	// 管理 API: 默认全部需要 Bearer 认证, api_auth_exempt 列表内的路由放行
	api := router.Group("/api", apiAuth(store))

	if cfg.Metrics.Enable {
		api.GET("/metrics", gin.WrapH(promhttp.Handler()))
	}

	api.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 线路状态与心跳统计
	api.GET("/entry", handleEntryStatus(store, tracker, balancer))

	// BDS 状态与健康统计
	api.GET("/bds", handleBDSStatus(store, bdsTrack))

	// 日志查看 (内存缓冲)
	api.GET("/log", handleLog)

	// 代理状态采样 (折线图)
	api.GET("/stats", stats.handleStats)

	// 会话列表 (含玩家信息与流量统计)
	api.GET("/session", handleSessionList(mux.Table()))
	// 掐断指定会话
	api.DELETE("/session/:ufrag", handleCloseSession(mux.Table()))

	registerConfigAPI(api, store)

	return &Gateway{
		store:  store,
		prober: prober,
		stats:  stats,
		srv: &http.Server{
			Addr:    net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
			Handler: router,
			// BDS 协商最长约 15s, 读超时留足余量
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       60 * time.Second,
			TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

// Start 启动网关服务, 阻塞直到服务停止或被关闭
func (g *Gateway) Start() error {
	cfg := g.store.Get().Gateway
	var err error
	switch {
	case cfg.TLS.Enable && cfg.TLS.Dual:
		err = g.startDual(cfg)
	case cfg.TLS.Enable:
		logger.Info("gateway listening with TLS", "addr", g.srv.Addr, "cert", cfg.TLS.Cert)
		err = g.srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
	default:
		logger.Info("gateway listening", "addr", g.srv.Addr)
		err = g.srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("gateway serve: %w", err)
}

// startDual 同端口同时提供明文 HTTP 与 HTTPS (按首字节分流)
func (g *Gateway) startDual(cfg conf.GatewayConf) error {
	cert, err := tls.LoadX509KeyPair(cfg.TLS.Cert, cfg.TLS.Key)
	if err != nil {
		return fmt.Errorf("load tls keypair: %w", err)
	}
	tlsCfg := g.srv.TLSConfig.Clone()
	tlsCfg.Certificates = []tls.Certificate{cert}
	tlsCfg.NextProtos = []string{"h2", "http/1.1"}

	ln, err := net.Listen("tcp", g.srv.Addr)
	if err != nil {
		return fmt.Errorf("gateway listen: %w", err)
	}
	g.dualLn.Store(&ln)
	plainLn, tlsLn := splitDualListener(ln, tlsCfg)
	logger.Info("gateway listening (dual http/https)", "addr", ln.Addr(), "cert", cfg.TLS.Cert)

	errCh := make(chan error, 2)
	go func() { errCh <- g.srv.Serve(plainLn) }()
	go func() { errCh <- g.srv.Serve(tlsLn) }()
	err = <-errCh
	// Shutdown 先关底层 listener, Serve 返回 net.ErrClosed 属正常关闭
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Shutdown 优雅关闭网关服务, 等待进行中的请求处理完毕
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.prober.close()
	g.stats.close()
	if ln := g.dualLn.Load(); ln != nil {
		_ = (*ln).Close() // 释放底层监听 (Shutdown 只管 chanListener)
	}
	return g.srv.Shutdown(ctx)
}

// accessLog 访问日志中间件, 输出到项目日志器
func accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("gateway request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", time.Since(start).String(),
			"client", c.ClientIP(),
		)
	}
}

// apiAuth 管理 API 认证中间件: api_auth_exempt 列表内的路由放行,
// 其余校验 Authorization: Bearer <gateway.token>。
// 豁免条目支持 "GET /api/healthz" (仅该方法) 或 "/api/healthz" (全部方法)。
// 豁免列表与 token 每次请求从配置中心读取, 热更即时生效;
// 使用常量时间比较防止时序攻击。
func apiAuth(store *conf.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := store.Get().Gateway
		if apiExempt(cfg.APIAuthExempt, c.Request.Method, c.FullPath()) {
			c.Next()
			return
		}
		expected := []byte("Bearer " + cfg.Token)
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), expected) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}

// apiExempt 判定路由是否豁免: 条目 "METHOD /path" 仅匹配该方法,
// "/path" 匹配全部方法 (按路由模板匹配)
func apiExempt(exempt []string, method, route string) bool {
	for _, e := range exempt {
		m, p, found := strings.Cut(e, " ")
		if found {
			if strings.EqualFold(m, method) && p == route {
				return true
			}
			continue
		}
		if e == route {
			return true
		}
	}
	return false
}

// collectMetrics 指标收集中间件, path 标签使用路由模板以避免高基数
func collectMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		status := strconv.Itoa(c.Writer.Status())
		requestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		requestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}

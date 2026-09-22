// Package gateway 网关 HTTP 服务
package gateway

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/multiplexer"
)

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
	store *conf.Store
	srv   *http.Server
}

// New 创建网关服务, 监听地址/TLS/metrics 开关取启动时配置, 之后重载不生效
func New(store *conf.Store, mux *multiplexer.Multiplexer, tracker *multiplexer.EntryTracker) *Gateway {
	cfg := store.Get().Gateway
	router := gin.New()
	router.Use(gin.Recovery(), accessLog())

	// NetherNet 信令端点, 客户端硬编码路径, 必须挂在根路径
	balancer := newEntryBalancer(store, mux.Table(), tracker)
	join := newJoinHandler(store, balancer, mux)
	router.GET("/v1/join", join.motd)
	router.POST("/v1/join/:networkID", join.offer)

	api := router.Group("/api")

	if cfg.Metrics.Enable {
		router.Use(collectMetrics())
		if cfg.Metrics.Auth {
			api.GET("/metrics", tokenAuth(store), gin.WrapH(promhttp.Handler()))
		} else {
			api.GET("/metrics", gin.WrapH(promhttp.Handler()))
		}
	}

	api.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 线路状态与心跳统计
	api.GET("/entry", tokenAuth(store), handleEntryStatus(store, tracker, balancer))

	// 会话列表 (含玩家信息与流量统计)
	api.GET("/session", tokenAuth(store), handleSessionList(mux.Table()))
	// 掐断指定会话
	api.DELETE("/session/:ufrag", tokenAuth(store), handleCloseSession(mux.Table()))

	registerConfigAPI(api, store)

	return &Gateway{
		store: store,
		srv: &http.Server{
			Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

// Start 启动网关服务, 阻塞直到服务停止或被关闭
func (g *Gateway) Start() error {
	cfg := g.store.Get().Gateway
	var err error
	if cfg.TLS.Enable {
		logger.Info("gateway listening with TLS", "addr", g.srv.Addr, "cert", cfg.TLS.Cert)
		err = g.srv.ListenAndServeTLS(cfg.TLS.Cert, cfg.TLS.Key)
	} else {
		logger.Info("gateway listening", "addr", g.srv.Addr)
		err = g.srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("gateway serve: %w", err)
}

// Shutdown 优雅关闭网关服务, 等待进行中的请求处理完毕
func (g *Gateway) Shutdown(ctx context.Context) error {
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

// tokenAuth 校验 Authorization: Bearer <gateway.token> 请求头, 使用常量时间比较防止时序攻击
// token 每次请求从配置中心读取, 重载后即时生效
func tokenAuth(store *conf.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		expected := []byte("Bearer " + store.Get().Gateway.Token)
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), expected) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
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

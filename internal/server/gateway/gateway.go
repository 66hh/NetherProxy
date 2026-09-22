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
	conf conf.GatewayConf
	srv  *http.Server
}

const apiPrefix = "/api"

// New 创建网关服务
func New(cfg conf.GatewayConf) *Gateway {
	router := gin.New()
	router.Use(gin.Recovery(), accessLog())

	api := router.Group(apiPrefix)

	if cfg.Metrics.Enable {
		router.Use(collectMetrics())
		if cfg.Metrics.Auth {
			api.GET("/metrics", tokenAuth(cfg.Token), gin.WrapH(promhttp.Handler()))
		} else {
			api.GET("/metrics", gin.WrapH(promhttp.Handler()))
		}
	}

	api.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	return &Gateway{
		conf: cfg,
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
	var err error
	if g.conf.TLS.Enable {
		logger.Info("gateway listening with TLS", "addr", g.srv.Addr, "cert", g.conf.TLS.Cert)
		err = g.srv.ListenAndServeTLS(g.conf.TLS.Cert, g.conf.TLS.Key)
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

// tokenAuth 校验 Authorization: Bearer <token> 请求头, 使用常量时间比较防止时序攻击
func tokenAuth(token string) gin.HandlerFunc {
	expected := []byte("Bearer " + token)
	return func(c *gin.Context) {
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

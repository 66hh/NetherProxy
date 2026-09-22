package gateway

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/multiplexer"
)

// registerConfigAPI 注册配置管理路由, 全部强制 Bearer 认证
//
//	GET  /api/config         读取当前生效配置
//	PUT  /api/config         校验并保存配置 (JSON), 保存成功后立即生效
//	POST /api/config/reload  从配置文件重新加载
func registerConfigAPI(api *gin.RouterGroup, store *conf.Store) {
	g := api.Group("/config", tokenAuth(store))
	g.GET("", handleReadConfig(store))
	g.PUT("", handleWriteConfig(store))
	g.POST("/reload", handleReloadConfig(store))
}

func handleReadConfig(store *conf.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, store.Get())
	}
}

func handleWriteConfig(store *conf.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var cfg conf.Conf
		if err := c.ShouldBindJSON(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
			return
		}
		old := store.Get()
		if err := store.Write(&cfg); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config rejected", "details": err.Error()})
			return
		}
		applyHot(store.Get())
		logger.Info("config written via api", "client", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"status": "ok", "restart_required": restartRequired(old, &cfg)})
	}
}

func handleReloadConfig(store *conf.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		old := store.Get()
		if err := store.Reload(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "reload failed", "details": err.Error()})
			return
		}
		applyHot(store.Get())
		logger.Info("config reloaded via api", "client", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"status": "ok", "restart_required": restartRequired(old, store.Get())})
	}
}

// handleEntryStatus 返回各线路的配置、路由状态与心跳统计
func handleEntryStatus(store *conf.Store, tracker *multiplexer.EntryTracker, balancer *entryBalancer) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats := tracker.Snapshot()
		counts := balancer.Snapshot()
		entries := make([]gin.H, 0)
		cfg := store.Get()
		for i := range cfg.Entry {
			e := &cfg.Entry[i]
			key := multiplexer.EntryKey(e)
			entry := gin.H{
				"key":             key,
				"enable":          e.Enable,
				"max_session":     e.MaxSession,
				"heartbeat":       e.Heartbeat,
				"healthy":         tracker.Healthy(key),
				"active_sessions": counts[key],
			}
			if s, ok := stats[key]; ok {
				entry["stats"] = s
			}
			entries = append(entries, entry)
		}
		c.JSON(http.StatusOK, gin.H{"entries": entries})
	}
}

// applyHot 应用支持热生效的配置项
func applyHot(cfg *conf.Conf) {
	// BDS 路由与 gateway.token 由使用方每次从 Store 读取, 天然热生效
	logger.SetLevel(cfg.Log.Level)
}

// restartRequired 判断新旧配置差异是否涉及只能重启生效的项:
// gateway 监听地址/TLS/metrics 开关, multiplexer 监听地址, 以及日志的输出格式与文件配置
func restartRequired(old, new *conf.Conf) bool {
	og, ng := old.Gateway, new.Gateway
	if og.Host != ng.Host || og.Port != ng.Port || og.TLS != ng.TLS || og.Metrics != ng.Metrics {
		return true
	}
	if old.Multiplexer != new.Multiplexer {
		return true
	}
	ol, nl := old.Log, new.Log
	ol.Level, nl.Level = "", "" // level 支持热更, 不参与比较
	return ol != nl
}

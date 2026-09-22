package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/gateway"
	"NetherProxy/internal/server/multiplexer"
	"NetherProxy/internal/session"
)

const (
	configPath = "config.yml"
	// entryStatsPath 是线路心跳统计的持久化文件
	entryStatsPath = "entry_stats.json"
)

func main() {

	cfg, err := conf.Load(configPath)

	if errors.Is(err, os.ErrNotExist) {

		// 配置文件不存在时生成默认配置并退出, 提示用户修改后再启动
		if err := conf.Save(configPath, conf.Default()); err != nil {
			fatal("failed to write default config", err)
		}

		fatal("default config generated at "+configPath+", please edit it and restart", nil)
	}

	if err != nil {
		fatal("failed to load config", err)
	}

	var w io.Writer = os.Stdout

	if cfg.Log.File != "" {

		f := logger.NewRotateWriter(cfg.Log.File, cfg.Log.MaxSize, cfg.Log.MaxBackups, cfg.Log.MaxAge, cfg.Log.Compress)
		defer f.Close()

		w = io.MultiWriter(os.Stdout, f)
	}

	logger.Init(cfg.Log.Level, cfg.Log.Format, w)

	logger.Info("NetherProxy started")

	store := conf.NewStore(configPath, cfg)
	table := session.NewTable(store)
	tracker := multiplexer.NewEntryTracker(entryStatsPath)

	mux := multiplexer.New(store, table, tracker)
	if err := mux.Start(); err != nil {
		logger.Error("multiplexer start failed", "err", err)
		os.Exit(1)
	}

	heartbeat := multiplexer.NewHeartbeat(store, tracker)
	heartbeat.Start()

	gw := gateway.New(store, mux, tracker)

	go func() {
		if err := gw.Start(); err != nil {
			logger.Error("gateway stopped with error", "err", err)
			os.Exit(1)
		}
	}()

	// 等待退出信号并优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Info("shutdown signal received", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := gw.Shutdown(ctx); err != nil {
		logger.Error("gateway shutdown error", "err", err)
	}

	// 逆序关闭: 先停探测与转发, 再回收会话, 最后落盘统计
	heartbeat.Close()
	if err := mux.Close(); err != nil {
		logger.Error("multiplexer close error", "err", err)
	}
	table.Close()
	tracker.Close()

	logger.Info("NetherProxy stopped")
}

// fatal 在日志器就绪前输出错误并退出
func fatal(msg string, err error) {

	if err != nil {
		_, _ = os.Stderr.WriteString(msg + ": " + err.Error() + "\n")
	} else {
		_, _ = os.Stderr.WriteString(msg + "\n")
	}

	os.Exit(1)
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
	logger.SetBufSize(cfg.Log.BufferSize)

	logger.Info("NetherProxy started")

	store := conf.NewStore(configPath, cfg)
	table := session.NewTable(store)
	tracker := multiplexer.NewEntryTracker(entryStatsPath, store)

	mux := multiplexer.New(store, table, tracker)
	if err := mux.Start(); err != nil {
		logger.Error("multiplexer start failed", "err", err)
		tracker.Close()
		os.Exit(1)
	}

	heartbeat := multiplexer.NewHeartbeat(store, tracker)
	heartbeat.Start()

	gw := gateway.New(store, mux, tracker)

	// 打印面板地址与 token
	panelHost := cfg.Gateway.Host
	if panelHost == "0.0.0.0" || panelHost == "::" || panelHost == "" {
		panelHost = "127.0.0.1"
	}
	scheme := "http"
	if cfg.Gateway.TLS.Enable {
		scheme = "https"
	}
	panelAddr := net.JoinHostPort(panelHost, fmt.Sprint(cfg.Gateway.Port))
	logger.Info("web panel starting", "url", fmt.Sprintf("%s://%s/", scheme, panelAddr))
	// token 只打印到控制台, 不进日志缓冲 (面板日志页可见日志缓冲)
	fmt.Printf("gateway token: %s\n", cfg.Gateway.Token)

	// gateway 启动错误经 channel 上报, 统一走优雅关闭路径
	errCh := make(chan error, 1)
	go func() {
		errCh <- gw.Start()
	}()

	// 等待退出信号; 第二次信号强制退出
	quit := make(chan os.Signal, 2)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	var serveErr error
	select {
	case sig := <-quit:
		logger.Info("shutdown signal received", "signal", sig.String())
		// 仅在收到第一次信号后才监听第二次信号强制退出
		go func() {
			<-quit
			logger.Warn("second signal received, forcing exit")
			os.Exit(1)
		}()
	case err := <-errCh:
		if err != nil {
			logger.Error("gateway stopped with error", "err", err)
			serveErr = err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := gw.Shutdown(ctx); err != nil {
		logger.Error("gateway shutdown error", "err", err)
	}

	// 逆序关闭: 先停探测, 再回收会话 (关闭 backend socket 使 backendLoop 退出),
	// 然后停数据面, 最后落盘统计
	heartbeat.Close()
	table.Close()
	if err := mux.Close(); err != nil {
		logger.Error("multiplexer close error", "err", err)
	}
	tracker.Close()

	logger.Info("NetherProxy stopped")

	// 启动失败以非零码退出, 便于守护进程识别
	if serveErr != nil {
		os.Exit(1)
	}
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

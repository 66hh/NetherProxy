// netherproxy 是 NetherNet 单端口复用边缘代理（文档 §7）。
//
// 公网仅暴露一个 UDP 端口承载全部玩家数据面；HTTP 信令透传到内网 BDS，
// 拦截 SDP answer 重写 candidate；DTLS/游戏加密端到端保持，代理不终止。
//
// 示例（本地联调，BDS 在 127.0.0.1:19132）：
//
//	netherproxy -http :29132 -udp :19133 -backend 127.0.0.1:19132 -advertise 127.0.0.1
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"

	"NetherProxy/internal/dataplane"
	"NetherProxy/internal/session"
	"NetherProxy/internal/signaling"
)

func main() {
	var (
		httpAddr   = flag.String("http", ":19132", "HTTP 信令监听地址（对外）")
		udpAddr    = flag.String("udp", ":19133", "UDP 数据面监听地址（对外，全部玩家复用此端口）")
		backend    = flag.String("backend", "127.0.0.1:19132", "内网 BDS 信令地址（host:port）")
		advertise  = flag.String("advertise", "", "answer 中广告的代理公网 IP（必填）")
		forceRelay = flag.Bool("force-relay", false, "重写 offer 中客户端 candidate 为不可达占位，强制数据面走代理（同局域网调试用）")
		debug      = flag.Bool("debug", false, "输出 debug 日志")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if *advertise == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -advertise（代理公网 IP）")
		os.Exit(2)
	}
	advertiseIP, err := netip.ParseAddr(*advertise)
	if err != nil {
		fmt.Fprintf(os.Stderr, "无效的 -advertise IP: %v\n", err)
		os.Exit(2)
	}
	if _, _, err := net.SplitHostPort(*backend); err != nil {
		fmt.Fprintf(os.Stderr, "无效的 -backend 地址: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	table := session.NewTable(log)
	defer table.Close()

	plane, err := dataplane.Listen(*udpAddr, table, log)
	if err != nil {
		log.Error("UDP 数据面监听失败", "addr", *udpAddr, "error", err)
		os.Exit(1)
	}
	defer plane.Close()

	proxy := signaling.NewProxy(*backend, advertiseIP, plane.Port(), *forceRelay, plane.CreateSession, log)

	errCh := make(chan error, 1)
	go func() {
		errCh <- proxy.ListenAndServe(ctx, *httpAddr)
	}()

	log.Info("netherproxy started",
		"http", *httpAddr,
		"udp", *udpAddr,
		"backend", *backend,
		"advertise", advertiseIP.String(),
	)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			log.Error("signaling proxy exited", "error", err)
			os.Exit(1)
		}
	}
}

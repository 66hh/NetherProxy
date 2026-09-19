// Package signaling 实现 NetherNet HTTP 信令线的透传代理。
//
// 所有请求原样透传到内网 BDS；唯独 POST /v1/join/{networkID} 的 SDP answer
// 响应会被拦截重写（candidate/c= 指向代理公网地址），并以 answer 中的
// ice-ufrag 为路由键创建数据面会话。参见文档 §5、§7.3、§7.4。
package signaling

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// maxSDPBodySize 与原版一致：SDP offer/answer 的 body 上限 1 MiB。
const maxSDPBodySize = 1 << 20

// CreateSessionFunc 在成功拦截并重写一个 SDP answer 后调用，
// 由数据面负责为该会话建立内部 socket 并注册到会话表。
type CreateSessionFunc func(ufrag string, backendAddr netip.AddrPort) error

// Proxy 是 HTTP 信令透传代理。
type Proxy struct {
	backend       string // 内网 BDS 信令地址（host:port）
	advertiseIP   netip.Addr
	publicPort    uint16
	forceRelay    bool
	createSession CreateSessionFunc
	log           *slog.Logger
	client        *http.Client
}

// NewProxy 创建信令代理。backend 形如 "192.168.1.5:19132"；
// advertiseIP/publicPort 是重写进 answer 的代理公网地址。
//
// forceRelay 为 true 时，offer 中的客户端 candidate 会被替换为不可达
// 占位地址，防止 BDS 与客户端同局域网时直接 check 成功而旁路代理
// （仅用于本地联调验证；公网部署无需开启）。
func NewProxy(backend string, advertiseIP netip.Addr, publicPort uint16, forceRelay bool, createSession CreateSessionFunc, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	return &Proxy{
		backend:       backend,
		advertiseIP:   advertiseIP,
		publicPort:    publicPort,
		forceRelay:    forceRelay,
		createSession: createSession,
		log:           log,
		client: &http.Client{
			// BDS 协商最长等待约 15s（NegotiationContext），留足余量。
			Timeout: 30 * time.Second,
		},
	}
}

// Handler 返回代理的 http.Handler，路由与原版端点一致。
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/join", p.handlePing)
	mux.HandleFunc("POST /v1/join/{networkID}", p.handleOffer)
	return mux
}

// ListenAndServe 在 addr 上启动 HTTP 服务（原版客户端会依次尝试
// https/http，裸 HTTP 模式供内网或前置 TLS 终止的场景使用）。
func (p *Proxy) ListenAndServe(ctx context.Context, addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	p.log.Info("signaling proxy listening", "addr", addr, "backend", p.backend)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// handlePing 透传 GET /v1/join（服务器名片）。
func (p *Proxy) handlePing(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r, nil, nil)
}

// handleOffer 透传 POST /v1/join/{networkID}，并拦截重写 SDP answer。
// forceRelay 开启时，先对 offer 的 candidate 做占位重写（防局域网旁路）。
func (p *Proxy) handleOffer(w http.ResponseWriter, r *http.Request) {
	var rewriteReq func(body []byte) []byte
	if p.forceRelay {
		rewriteReq = func(body []byte) []byte {
			if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("v=")) {
				return body
			}
			p.log.Debug("original offer", "len", len(body), "sdp", string(body))
			rewritten, err := RewriteOffer(body)
			if err != nil {
				p.log.Error("rewrite offer failed, passing through", "error", err)
				return body
			}
			p.log.Debug("rewritten offer", "len", len(rewritten), "sdp", string(rewritten))
			return rewritten
		}
	}
	p.forward(w, r, rewriteReq, p.maybeRewriteAnswer)
}

// forward 把请求透传到内网 BDS 并回写响应；rewriteReq 非 nil 时先改写
// 请求 body，rewriteResp 非 nil 时对响应 body 做拦截处理。
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, rewriteReq func(body []byte) []byte, rewriteResp func(status int, contentType string, body []byte) []byte) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSDPBodySize))
	if err != nil {
		writeText(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if rewriteReq != nil {
		body = rewriteReq(body)
	}

	url := "http://" + p.backend + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, bytes.NewReader(body))
	if err != nil {
		writeText(w, http.StatusInternalServerError, "failed to build backend request")
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Connection")

	resp, err := p.client.Do(req)
	if err != nil {
		p.log.Error("backend request failed", "method", r.Method, "url", r.URL, "error", err)
		writeText(w, http.StatusBadGateway, "backend unreachable")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSDPBodySize+1))
	if err != nil {
		writeText(w, http.StatusBadGateway, "failed to read backend response")
		return
	}
	if rewriteResp != nil {
		respBody = rewriteResp(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// maybeRewriteAnswer 拦截 200/application/sdp 的 answer：
// 重写 candidate 并注册数据面会话。数字错误码（纯数字 body）与其他
// 内容原样透传。
func (p *Proxy) maybeRewriteAnswer(status int, contentType string, body []byte) []byte {
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/sdp") {
		return body
	}
	// 拦截前确认 body 真是 SDP（answer 也可能是纯数字错误码）。
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("v=")) {
		return body
	}

	p.log.Debug("original answer", "len", len(body), "sdp", string(body))
	res, err := RewriteAnswer(body, p.advertiseIP, p.publicPort)
	if err != nil {
		p.log.Error("rewrite answer failed, passing through", "error", err)
		return body
	}
	p.log.Debug("rewritten answer", "len", len(res.Body), "sdp", string(res.Body))
	if err := p.createSession(res.Ufrag, res.BackendAddr); err != nil {
		// 会话建立失败时回传原始 answer：客户端拿到的内网地址必然连不通，
		// 但保留原始响应便于排障，也避免代理与 BDS 行为分叉。
		p.log.Error("create session failed, passing through", "ufrag", res.Ufrag, "backend", res.BackendAddr, "error", err)
		return body
	}
	p.log.Info("session signaled", "ufrag", res.Ufrag, "backend", res.BackendAddr)
	return res.Body
}

// writeText 以 text/plain 回写错误响应。
func writeText(w http.ResponseWriter, statusCode int, text string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(text))
}

// BackendAddr 返回内网 BDS 的信令地址。
func (p *Proxy) BackendAddr() string { return p.backend }

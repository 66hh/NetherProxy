package gateway

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"NetherProxy/internal/conf"
	"NetherProxy/internal/logger"
	"NetherProxy/internal/server/multiplexer"
	"NetherProxy/internal/session"
)

// joinBodyLimit 是信令请求的 body 上限, 与原版一致 1 MiB
const joinBodyLimit = 1 << 20

// joinHandler 处理 NetherNet HTTP 信令端点 (/v1/join),
// 按请求 Host 匹配 BDS 配置并透传; 后端列表从配置中心动态读取, 重载即时生效
type joinHandler struct {
	store    *conf.Store
	balancer *entryBalancer
	mux      *multiplexer.Multiplexer
	verifier *identityVerifier
	limiter  *rateLimiter
	client   *http.Client
}

func newJoinHandler(store *conf.Store, balancer *entryBalancer, mux *multiplexer.Multiplexer) *joinHandler {
	return &joinHandler{
		store:    store,
		balancer: balancer,
		mux:      mux,
		verifier: newIdentityVerifier(),
		limiter:  newRateLimiter(),
		client: &http.Client{
			// BDS 协商最长等待约 15s, 留足余量
			Timeout: 30 * time.Second,
		},
	}
}

// motd 处理 GET /v1/join, 透传服务器名片; 按客户端 IP 限流防止打爆 BDS
func (h *joinHandler) motd(c *gin.Context) {
	if rl := h.store.Get().Gateway.RateLimit; rl.Enable && !h.limiter.allow("ip:"+c.ClientIP(), rl) {
		logger.Warn("motd rate limited", "client", c.ClientIP())
		writeText(c, http.StatusTooManyRequests, "rate limited")
		return
	}
	backend := h.match(c.Request.Host)
	if backend == nil {
		logger.Warn("no bds route for host", "host", c.Request.Host)
		writeText(c, http.StatusNotFound, "no route for host")
		return
	}
	h.forward(c, backend, nil, nil)
}

// offer 处理 POST /v1/join/:networkID, 透传 SDP offer 并拦截重写 answer
func (h *joinHandler) offer(c *gin.Context) {
	backend := h.match(c.Request.Host)
	if backend == nil {
		logger.Warn("no bds route for host", "host", c.Request.Host)
		writeText(c, http.StatusNotFound, "no route for host")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, joinBodyLimit))
	if err != nil {
		writeText(c, http.StatusBadRequest, "failed to read request body")
		return
	}
	logger.Debug("join offer request", "network_id", c.Param("networkID"), "host", c.Request.Host, "backend", net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)), "sdp", string(body))

	// relay_only 中继模式: 隐藏客户端真实地址, 强制全部流量经代理;
	// 该模式承诺不泄露客户端地址, 重写失败时必须拒绝而非透传
	relayOnly := h.store.Get().Gateway.RelayOnly
	if relayOnly {
		rewritten, err := RewriteOffer(body)
		if err != nil {
			logger.Error("rewrite offer failed, rejected (relay_only)", "err", err)
			c.Data(http.StatusOK, "application/sdp", []byte("37"))
			return
		}
		body = rewritten
	}

	// 验证玩家身份 (offer 中微软签发的 identity JWT), 不转发到 BDS;
	// 拒绝时返回与 BDS 一致的数字错误码 37 (IdentityNotAllowed)。
	// verify_identity 关闭时仅解析玩家信息用于展示, 不做验签。
	var player PlayerInfo
	verified := false
	if h.store.Get().Gateway.VerifyIdentity {
		player, err = h.verifier.verify(body)
		if err != nil {
			logger.Warn("identity rejected", "client", c.ClientIP(), "err", err)
			c.Data(http.StatusOK, "application/sdp", []byte("37"))
			return
		}
		verified = true
	} else {
		player = h.verifier.extractPlayer(body)
	}

	// join 频率限制: 已验签按 XUID, 未验签回退按客户端 IP
	rateKey := player.XUID
	if !verified {
		rateKey = "ip:" + c.ClientIP()
	}
	if rl := h.store.Get().Gateway.RateLimit; rl.Enable && !h.limiter.allow(rateKey, rl) {
		logger.Warn("join rate limited", "client", c.ClientIP(), "player", player.Name, "key", rateKey)
		c.Data(http.StatusOK, "application/sdp", []byte("37"))
		return
	}

	// 黑白名单与 webhook 依赖可信身份, 未验签时启用即拒绝 (fail-closed)
	if !verified {
		acc := h.store.Get().Gateway.Access
		if acc.Mode == "blacklist" || acc.Mode == "whitelist" || acc.Webhook.Enable {
			logger.Warn("access control requires verified identity, rejected",
				"client", c.ClientIP(), "player", player.Name)
			c.Data(http.StatusOK, "application/sdp", []byte("37"))
			return
		}
	} else if ok, reason := h.checkAccess(player, c.ClientIP()); !ok {
		logger.Warn("access denied", "client", c.ClientIP(), "player", player.Name, "xuid", player.XUID, "reason", reason)
		c.Data(http.StatusOK, "application/sdp", []byte("37"))
		return
	}

	h.forward(c, backend, body, func(status int, contentType string, respBody []byte) []byte {
		return h.rewriteAnswer(status, contentType, respBody, player)
	})
}

// rewriteAnswer 拦截 200/application/sdp 的 answer: 分配公网线路, 重写
// candidate 并注册数据面会话。数字错误码 (纯数字 body) 与其他内容原样透传。
// 重写/建会话失败时同样透传原始 answer, 保留 BDS 原始行为便于排障。
func (h *joinHandler) rewriteAnswer(status int, contentType string, body []byte, player PlayerInfo) []byte {
	if status != http.StatusOK || !strings.HasPrefix(contentType, "application/sdp") {
		return body
	}
	// 拦截前确认 body 真是 SDP (answer 也可能是纯数字错误码)
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("v=")) {
		return body
	}

	entry, err := h.balancer.pick()
	if err != nil {
		logger.Error("no available entry, passing through", "err", err)
		return h.passThrough(body, "")
	}
	key := multiplexer.EntryKey(entry)

	ip, err := resolveEntryIP(entry.Host)
	if err != nil {
		logger.Error("resolve entry host failed, passing through", "entry", key, "err", err)
		return h.passThrough(body, key)
	}
	res, err := RewriteAnswer(body, ip, uint16(entry.Port))
	if err != nil {
		logger.Error("rewrite answer failed, passing through", "err", err)
		return h.passThrough(body, key)
	}
	logger.Debug("answer rewritten", "ufrag", res.Ufrag, "backend", res.BackendAddr,
		"original", string(body), "rewritten", string(res.Body))
	info := session.SessionInfo{
		Ufrag:       res.Ufrag,
		Pwd:         res.Pwd,
		Player:      player.Name,
		XUID:        player.XUID,
		BackendAddr: res.BackendAddr,
		Entry:       key,
	}
	if err := h.mux.CreateSession(info); err != nil {
		logger.Error("create session failed, passing through", "ufrag", res.Ufrag, "backend", res.BackendAddr, "err", err)
		return h.passThrough(body, key)
	}
	logger.Info("session signaled", "ufrag", res.Ufrag, "player", player.Name, "xuid", player.XUID, "backend", res.BackendAddr, "entry", key)
	return res.Body
}

// passThrough 处理重写/建会话失败的回退: 释放线路计数;
// relay_only 模式承诺流量必经代理, 返回 nil 表示拒绝本次 join,
// 普通模式透传原始 answer 保留 BDS 原始行为便于排障
func (h *joinHandler) passThrough(body []byte, entryKey string) []byte {
	if entryKey != "" {
		h.balancer.release(entryKey)
	}
	if h.store.Get().Gateway.RelayOnly {
		return nil
	}
	return body
}

// match 按请求 Host 匹配 BDS 配置, 仅匹配启用的条目, 列表中靠前的优先
func (h *joinHandler) match(hostport string) *conf.BDSConf {
	host := hostWithoutPort(hostport)
	cfg := h.store.Get()
	for i := range cfg.BDS {
		if cfg.BDS[i].Enable && cfg.BDS[i].MatchDomain(host) {
			return &cfg.BDS[i]
		}
	}
	return nil
}

// forward 把请求透传到匹配的 BDS 并回写响应; body 为 nil 时使用原始请求体
// (GET), 否则使用给定内容 (POST 已读取的 offer); rewriteResp 非空时对响应
// body 做拦截处理
func (h *joinHandler) forward(c *gin.Context, backend *conf.BDSConf, body []byte, rewriteResp func(status int, contentType string, respBody []byte) []byte) {
	target := url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)),
		Path:     c.Request.URL.Path,
		RawQuery: c.Request.URL.RawQuery,
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target.String(), reqBody)
	if err != nil {
		writeText(c, http.StatusInternalServerError, "failed to build backend request")
		return
	}
	copyHeaders(req.Header, c.Request.Header)
	req.Host = c.Request.Host

	resp, err := h.client.Do(req)
	if err != nil {
		logger.Error("bds request failed", "method", c.Request.Method, "backend", target.Host, "err", err)
		writeText(c, http.StatusBadGateway, "backend unreachable")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, joinBodyLimit+1))
	if err != nil {
		writeText(c, http.StatusBadGateway, "failed to read backend response")
		return
	}
	if len(respBody) > joinBodyLimit {
		logger.Warn("backend response too large", "backend", target.Host, "limit", joinBodyLimit)
		writeText(c, http.StatusBadGateway, "backend response too large")
		return
	}
	logger.Debug("bds response", "method", c.Request.Method, "backend", target.Host,
		"status", resp.StatusCode, "content_type", resp.Header.Get("Content-Type"), "body", string(respBody))
	if rewriteResp != nil {
		respBody = rewriteResp(resp.StatusCode, resp.Header.Get("Content-Type"), respBody)
		if respBody == nil {
			// relay_only 等模式下重写失败拒绝透传
			writeText(c, http.StatusBadGateway, "answer rejected")
			return
		}
	}

	copyHeaders(c.Writer.Header(), resp.Header)
	// body 可能已被改写, 原 Content-Length 不再有效, 删除后由 Go 按实际长度重写
	c.Writer.Header().Del("Content-Length")
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(respBody)
}

// copyHeaders 复制头部并剔除逐跳头
func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// isHopHeader 判定逐跳头 (RFC 2616 §13.5.1)
func isHopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}

// hostWithoutPort 去掉 Host 头中的端口部分, 兼容 IPv6 字面量
func hostWithoutPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

// writeText 以 text/plain 回写错误响应
func writeText(c *gin.Context, status int, text string) {
	c.Data(status, "text/plain; charset=utf-8", []byte(text))
}

package gateway

import (
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
)

// joinBodyLimit 是信令请求的 body 上限, 与原版一致 1 MiB
const joinBodyLimit = 1 << 20

// joinHandler 处理 NetherNet HTTP 信令端点 (/v1/join),
// 按请求 Host 匹配 BDS 配置并透传; 后端列表从配置中心动态读取, 重载即时生效
type joinHandler struct {
	store  *conf.Store
	client *http.Client
}

func newJoinHandler(store *conf.Store) *joinHandler {
	return &joinHandler{
		store: store,
		client: &http.Client{
			// BDS 协商最长等待约 15s, 留足余量
			Timeout: 30 * time.Second,
		},
	}
}

// motd 处理 GET /v1/join, 透传服务器名片
func (h *joinHandler) motd(c *gin.Context) {
	h.forward(c)
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

// forward 把请求透传到匹配的 BDS 并流式回写响应
func (h *joinHandler) forward(c *gin.Context) {
	backend := h.match(c.Request.Host)
	if backend == nil {
		logger.Warn("no bds route for host", "host", c.Request.Host)
		writeText(c, http.StatusNotFound, "no route for host")
		return
	}

	target := url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(backend.Host, strconv.Itoa(backend.Port)),
		Path:     c.Request.URL.Path,
		RawQuery: c.Request.URL.RawQuery,
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target.String(), c.Request.Body)
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

	copyHeaders(c.Writer.Header(), resp.Header)
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
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

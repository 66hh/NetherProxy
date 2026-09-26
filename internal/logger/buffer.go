package logger

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry 一条内存日志
type LogEntry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs,omitempty"`
}

var logBuf = &ringBuffer{capacity: 1000}

// SetBufSize 调整内存日志缓冲容量, 缩容时立即裁剪存量
func SetBufSize(n int) {
	if n > 0 {
		logBuf.mu.Lock()
		logBuf.capacity = n
		if len(logBuf.buf) > n {
			logBuf.buf = logBuf.buf[len(logBuf.buf)-n:]
		}
		logBuf.mu.Unlock()
	}
}

// TailLog 返回最近 n 条不小于 minLevel 的日志 (按时间正序)
func TailLog(n int, minLevel string) []LogEntry {
	if n <= 0 {
		return nil
	}
	min := parseLevel(minLevel)
	logBuf.mu.RLock()
	defer logBuf.mu.RUnlock()
	out := make([]LogEntry, 0, n)
	for i := len(logBuf.buf) - 1; i >= 0 && len(out) < n; i-- {
		e := logBuf.buf[i]
		lv, _ := parseLevelE(e.Level)
		if lv >= min {
			out = append(out, e)
		}
	}
	// 反转为时间正序
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func parseLevelE(s string) (slog.Level, bool) {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug, true
	case "INFO":
		return slog.LevelInfo, true
	case "WARN":
		return slog.LevelWarn, true
	case "ERROR":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

type ringBuffer struct {
	mu       sync.RWMutex
	buf      []LogEntry
	capacity int
}

func (r *ringBuffer) append(e LogEntry) {
	r.mu.Lock()
	r.buf = append(r.buf, e)
	if len(r.buf) > r.capacity {
		r.buf = r.buf[len(r.buf)-r.capacity:]
	}
	r.mu.Unlock()
}

// bufHandler 包装 slog.Handler, 同时写入内存缓冲
type bufHandler struct {
	slog.Handler
	attrs []slog.Attr // WithAttrs 绑定的属性 (Handle 时一并写入缓冲)
}

// WithAttrs 保持缓冲包装并累积绑定属性, 防止派生 logger 绕过内存缓冲
func (h bufHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return bufHandler{h.Handler.WithAttrs(attrs), append(h.attrs, attrs...)}
}

// WithGroup 保持缓冲包装
func (h bufHandler) WithGroup(name string) slog.Handler {
	return bufHandler{h.Handler.WithGroup(name), h.attrs}
}

func (h bufHandler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	writeAttr := func(a slog.Attr) {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
	}
	for _, a := range h.attrs {
		writeAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(a)
		return true
	})
	logBuf.append(LogEntry{Time: r.Time, Level: r.Level.String(), Msg: r.Message, Attrs: sb.String()})
	return h.Handler.Handle(ctx, r)
}

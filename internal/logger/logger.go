package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	global   = slog.Default()
	levelVar = new(slog.LevelVar)
)

// Init 初始化全局日志器
// level: debug/info/warn/error, format: text/json, w 为 nil 时输出到标准输出
func Init(level string, format string, w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	SetLevel(level)
	opts := &slog.HandlerOptions{Level: levelVar}
	var handler slog.Handler
	if strings.EqualFold(format, "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	global = slog.New(handler)
	slog.SetDefault(global)
}

// SetLevel 动态调整日志级别, 配置热重载时使用
func SetLevel(level string) {
	levelVar.Set(parseLevel(level))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// L 返回全局日志器, 用于需要 *slog.Logger 的场景
func L() *slog.Logger {
	return global
}

// NewRotateWriter 返回按大小轮转的日志文件写入器
// maxSize 单位 MB, maxBackups 为保留的旧文件数量上限, maxAge 为旧文件保留天数, 后两者为 0 表示不限制
func NewRotateWriter(path string, maxSize, maxBackups, maxAge int, compress bool) io.WriteCloser {
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSize,
		MaxBackups: maxBackups,
		MaxAge:     maxAge,
		Compress:   compress,
	}
}

func Debug(msg string, args ...any) {
	global.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	global.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	global.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	global.Error(msg, args...)
}

// Package buildinfo 构建信息, 由 CI 通过 -ldflags -X 注入; 本地开发构建为默认值
package buildinfo

var (
	// Version 版本号 (CI 注入 tag, 去掉 v 前缀)
	Version = "dev"
	// Commit 构建的 git commit 短哈希
	Commit = "none"
)

# AGENTS.md — NetherProxy 项目规范与易错点

面向 AI 代理/贡献者的工作规范。改动代码前必读。

## 项目速览

Minecraft 基岩版 NetherNet（WebRTC: ICE+DTLS+SCTP）单端口复用代理。
Go 后端（`cmd/` + `internal/`）+ React 面板（`web/`，构建产物内嵌到
`internal/server/gateway/web/index.html`）+ Python e2e（`tests/`）。

## 硬性规则

1. **日志一律英文**（slog 消息与字段值）；面板文案一律走 `web/src/i18n.js` 字典，禁止硬编码。
2. **禁止向仓库提交隐私信息**：真实 token、XUID、IP 不入代码/文档/测试。`config.yml`、`entry_stats.json` 已在 .gitignore。
3. **硬编码常量必须配置化**（加进 `Conf` 并校验），用户明确要求过。
4. **调试功能不入正式代码**（曾经为此移除过临时调试转发）。
5. **测试脚本不得包含真实凭据**，用模拟值。

## 添加/修改配置项的完整清单（最易漏）

新增一个配置字段时需要同步修改以下所有位置，缺一不可：

- `internal/conf/conf.go`：结构体字段（yaml+json tag 与注释）+ `Validate()` 校验
- `internal/conf/load.go`：`Default()` 默认值 + `normalize()` 数组/标量缺省填充
- `web/src/pages/Config.jsx`：`SCHEMA` 加字段（group/label 用 i18n key）
- `web/src/i18n.js`：**四个**语言字典（zh/en/ja/ko）都加翻译
- `docs/config.md`：配置文档
- 若影响启动行为：检查 `config_api.go` 的 `restartRequired()` 对比逻辑

新增管理 API 时：

- 路由挂在 `/api` 组（默认需 Bearer 认证）
- 写操作路由必须加入 `conf.go` 的豁免黑名单校验（防止被 `api_auth_exempt` 豁免成免认证）
- `docs/api.md` 同步

## 构建与验证

- **面板改动后必须** `cd web && npm run build`（产物 embed 进二进制），再 `go build`
- Go 侧验证：`go build ./... && go vet ./...`，跨平台加 `GOOS=linux go build ./...`
- e2e：`python tests/run_test.py`（需要代理在运行），9 个 PASS 为全通过
- **Windows 重建顺序**（顺序反了会跑旧二进制）：
  `taskkill //F //IM netherproxy.exe` → 等 1s → `rm -f netherproxy.exe~` → `go build -o netherproxy.exe ./cmd/netherproxy` → 后台启动
- 提交前清理临时文件：`netherproxy.exe~`、`proxy_run.log`、`tests/fake_bds.out` 等

## 架构易错点

- **BDS 端口池**：BDS 预绑定 19132–19139 到每张网卡地址，multiplexer 端口落进池内将收不到包（OS 按更具体绑定优先投递 UDP）。
- **5-tuple 安全**：只有通过 STUN MESSAGE-INTEGRITY 校验的包才能学习/改绑客户端地址，勿放宽。
- **relay_only 承诺**：该模式重写失败必须拒绝而非透传（防止泄露客户端真实地址）。
- **dual 模式**：TLS 在 listener 层终止，`c.Request.TLS` 恒为 nil；不要给 TLS 配置加 `h2`（`srv.Serve` 不自动装配 HTTP/2）。
- **时间戳**：Go `time.Time` JSON 序列化为 RFC3339Nano，前端 JS `Date` 只认毫秒——前端一律用 `api.js` 的 `parseTime()` 解析。
- **锁顺序**：tracker/balancer 的锁内不得调用会取 `store` 锁的阻塞操作（webhook 等发送必须锁外异步）。

# AGENTS.md

本文件面向在本仓库工作的 AI/开发者，说明项目约束、常用命令与发布流程。

## 项目概览

netgw 是单二进制的 HTTP(S) 反向代理网关：命中 `routes.http` 前缀的 HTTP
请求走反向代理，其余连接按原始 TCP 透传到 `backend`；配置 `tls_cert` 与
`tls_key` 后同一端口同时服务 HTTPS 与原始 TCP（按连接首字节嗅探）。

- Go 1.23+，**仅使用标准库**，不存在 `go.sum`，不要随意引入第三方依赖。
- 配置解析器是项目内置的 YAML 子集（两层嵌套映射、标量、双引号、`#` 注释），
  不支持列表、锚点、多行字符串。
- 入口 `cmd/gateway/main.go`，核心逻辑在 `internal/{config,gateway,proxy}`。

## 目录

```text
cmd/gateway/        入口：配置加载、日志、信号处理
internal/config/    配置结构、校验、内置 YAML 子集解析器、跨平台路径
internal/gateway/   监听、TLS 嗅探、原始 TCP 透传、证书热重载、优雅退出
internal/proxy/     前缀路由与 httputil.ReverseProxy
deploy/             systemd 单元
scripts/            构建、打包、产物校验
.github/workflows/  Tag 触发的发布流水线
```

## 关键约定

- 配置目录名由 `internal/config/paths.go` 的 `ConfigDirName` 决定；改名需同步
  打包脚本、systemd 单元、README 与测试。
- `tls_cert` / `tls_key` 必须同时配置；只配一项直接报错，证书加载失败也不静默
  降级为 HTTP。
- `backend` 是原始 TCP 透传目标，不是 HTTP 默认后端；未命中前缀的 HTTP 请求
  也会透传到该地址。
- 发行包内置的 `config.yaml` 是通用模板；个人配置放 `config.local.yaml`
  （已 gitignore，**不会自动加载**，需 `-config config.local.yaml`）。
- DEB/RPM 安装后以 `netgw` 用户运行且 `ProtectHome=true`，TLS 证书必须放在
  可读路径并使用绝对路径（如 `/etc/netgw/`）。
- RPM 脚本内联 `systemctl`，不要使用 `%systemd_post` 等宏：CI 在 Ubuntu 上
  构建，构建机没有 systemd-rpm-macros，宏会以原文留在脚本里导致安装失败。
- 修改配置语义时必须同步：`internal/config` 校验、README、默认模板
  `config.yaml`、`cmd/gateway/main.go` 的 `defaultConfigYAML`。

## 常用命令

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .                 # 仓库存在历史未格式化文件，勿顺手全量格式化

make build                 # 本机二进制
make linux-amd64           # 交叉编译
make release-zip VERSION=1.2.0

# 手动打包（产物名必须带版本号）
./scripts/build.sh linux amd64 1.2.0 /tmp/netgw-1.2.0-linux-amd64
./scripts/pack-zip.sh linux amd64 1.2.0 /tmp/netgw-1.2.0-linux-amd64 /tmp/netgw-1.2.0-linux-amd64.zip
./scripts/pack-linux-pkg.sh deb amd64 1.2.0 /tmp/netgw-1.2.0-linux-amd64 /tmp/netgw-1.2.0-linux-amd64.deb
./scripts/verify-artifacts.sh dist 1.2.0
```

## 发布流程

1. 本地验证：`go vet ./...`、`go test -race ./...`、`bash -n scripts/*.sh`。
2. 提交所有改动，工作区保持干净。
3. 打 Tag 并推送（版本号不带 `v` 前缀用于产物命名）：

   ```bash
   git tag -a v1.2.0 -m "netgw v1.2.0"
   git push origin master
   git push origin v1.2.0
   ```

4. `.github/workflows/release.yml` 自动执行：
   测试 → 6 平台构建 → 打包 ZIP/DEB/RPM → 产物校验 → 创建 Release。
5. Release 说明自动汇总上个 Tag 以来的提交，并附完整变更链接；已存在的
   Release 会覆盖资产并更新说明。
6. 产物统一命名 `netgw-<版本>-<os>-<arch>.<ext>`，例如
   `netgw-1.2.0-linux-amd64.deb`；文件名、DEB/RPM 元数据与
   `netgw -version` 输出中的版本号必须一致。

## 验证要求

- 修改 Go 代码后至少执行 `go vet ./...` 与 `go test ./...`，涉及并发/连接
  逻辑再跑 `go test -race ./...`。
- 修改 `scripts/` 或 workflow 后执行 `bash -n scripts/*.sh`，并尽量本地完整
  打包一次、用 `verify-artifacts.sh` 校验。
- 修改安装布局或脚本时，检查 DEB/RPM 的 `conffiles`/`noreplace` 与 systemd
  单元路径是否仍然一致。
- 未实际执行的检查不要声称通过。

## 代码风格

- 注释使用中文；保持现有结构与最小修改，不顺手重构无关代码。
- 不更改公开行为（配置字段、路由语义、安装布局）时需同步文档与测试。
- 新增配置项要提供默认值并补充 `internal/config` 测试。

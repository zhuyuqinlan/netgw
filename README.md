# netgw

轻量级、高性能、可独立部署的 **Go HTTP(S) 反向代理网关**，仅依赖标准库，单二进制即可运行。

网关默认监听 `0.0.0.0:8000`：命中路径前缀的 HTTP 请求走反向代理，其余连接按原始 TCP 透传到默认后端。配置 TLS 证书后同一端口同时服务 HTTPS 与原始 TCP——按连接首字节嗅探，TLS 在网关终止，非 TLS 连接（如 Minecraft）原样透传。

## 特性

- **前缀路由**：最长前缀优先，可选剥掉前缀。
- **默认后端**：路由未命中时转发到顶层 `backend`。
- **完整代理语义**：基于 `httputil.ReverseProxy`，支持 Keep-Alive、流式请求/响应、WebSocket、大文件上传，后端不可用返回 502。
- **HTTPS + TCP 混合端口**：TLS 按连接嗅探，非 TLS 协议零影响。
- **零第三方依赖**：内置 YAML 子集解析器，无 `go.sum`。
- **优雅退出**：SIGINT/SIGTERM 后停止接收新连接并排空活跃请求。

## 快速开始

环境要求：Go 1.23+。

```bash
go build -o netgw ./cmd/gateway
./netgw -config config.yaml
```

## 下载与安装

推送到 `v*` Tag 会自动构建并发布 GitHub Release：

| 文件 | 平台 |
|---|---|
| `netgw-<版本>-windows-{amd64,arm64}.zip` | Windows |
| `netgw-<版本>-linux-{amd64,arm64}.zip` | Linux 便携包 |
| `netgw-<版本>-linux-{amd64,arm64}.deb` | Debian/Ubuntu |
| `netgw-<版本>-linux-{amd64,arm64}.rpm` | RHEL/Fedora/SUSE |
| `netgw-<版本>-darwin-{amd64,arm64}.zip` | macOS |

产物文件名、包元数据与 `netgw -version` 输出中的版本号一致。

### ZIP 便携式运行

解压即用，不注册服务、不写入系统目录：

```bash
unzip netgw-<版本>-linux-amd64.zip
cd netgw
./netgw                        # 使用同目录 config.yaml
./netgw -config /path/to.yaml  # 或显式指定
```

macOS 首次运行若被 Gatekeeper 拦截，可执行 `xattr -d com.apple.quarantine netgw`。

### DEB / RPM 安装

```bash
sudo dpkg -i netgw-<版本>-linux-amd64.deb   # Debian / Ubuntu
sudo rpm -i netgw-<版本>-linux-amd64.rpm    # RHEL / Fedora / SUSE
```

安装布局：

```text
/usr/lib/netgw/netgw                  可执行文件
/etc/netgw/config.yaml                系统级配置（升级不覆盖）
/usr/share/doc/netgw/                 文档与 MIT 协议
/usr/lib/systemd/system/netgw.service systemd 服务单元
```

- 仅 DEB/RPM 注册 systemd 服务，不创建 `/usr/bin` 软链接。
- 程序以专用非 root 用户 `netgw` 运行。
- 安装后自动 enable（不自动 start），启动前按需修改 `/etc/netgw/config.yaml`。
- 安装包自带通用模板（监听 `0.0.0.0:8000`、无 TLS）；升级时已有配置不被覆盖。
  TLS 证书请放在服务账号可读的位置（如 `/etc/netgw/`，`chown root:netgw`、`chmod 640`）
  并在配置中使用**绝对路径**：服务以 `netgw` 用户运行且启用 `ProtectHome=true`，
  不要指向 `/root` 或用户家目录。

```bash
sudo systemctl start netgw
sudo systemctl enable netgw
journalctl -u netgw -f
```

### 升级与卸载

```bash
# ZIP：直接替换可执行文件，配置不受影响
sudo dpkg -i netgw-<版本>-linux-amd64.deb   # DEB 升级
sudo apt purge netgw                       # DEB 彻底清除（含配置）
sudo rpm -U netgw-<版本>-linux-amd64.rpm    # RPM 升级
sudo rpm -e netgw                    # RPM 卸载（保留配置）
```

## 配置

`config.yaml`（所有字段均有默认值）：

```yaml
server:
  listen: "0.0.0.0:8000"       # 监听地址
  read_header_timeout: 10s     # 请求头读取期限，防慢连接占用
  # tls_cert: "cert.pem"       # 同时配置证书与私钥即启用 HTTPS
  # tls_key: "key.pem"

http:
  connect_timeout: 5s          # 连接后端超时
  tls_skip_verify: false       # HTTPS 后端使用自签证书时设为 true（仅测试）

backend: "127.0.0.1:8080"      # 未命中路由时的默认后端

routes:
  http:
    "/api": "127.0.0.1:8080"   # 简写：前缀保留
    "/files":                  # 完整写法：可剥前缀
      backend: "127.0.0.1:8081"
      strip_prefix: true       # /files/x → 后端收到 /x

logging:
  level: "info"                # debug / info / warn / error
```

### 配置加载优先级

1. 命令行 `-config` 指定的文件（必须存在）。
2. 用户级配置：Windows `%APPDATA%/netgw/config.yaml`；Linux/macOS `~/.config/netgw/config.yaml`。
3. 系统级配置：`/etc/netgw/config.yaml`（Windows 无）。
4. 工作目录下的 `config.yaml`。
5. 内置默认值。

配置文件存在但解析失败时直接报错退出。`netgw -install-config` 可生成用户级默认配置，已存在则不覆盖。

### 路由规则

- **最长前缀优先**：`/api` 与 `/api/v1` 并存时，`/api/v1/x` 命中后者；`/apiweb` 会命中 `/api`（纯字符串前缀）。
- **strip_prefix**：只重写路径（`/api/users` → `/users`），查询串保留；剥完为空则转发为 `/`。
- **安全**：转发目标只来自配置文件，客户端无法指定，不存在开放代理风险。
- **转发头**：自动设置 `X-Forwarded-For` 与 `X-Forwarded-Proto`。

### HTTPS 混合端口

同时配置 `server.tls_cert` 与 `server.tls_key`（PEM）即启用。首字节是 TLS handshake record（`0x16 0x03`）的连接完成 TLS 握手后进入 HTTP 分流，其余连接原样透传。浏览器与 MC 客户端可共用同一端口。

生成自签证书（仅测试）：

```bash
go run crypto/tls/generate_cert.go --host 127.0.0.1
```

> 内置解析器支持嵌套映射、双引号键值、`#` 注释，不支持列表、锚点、多行字符串等完整 YAML 语法。

## 构建与发布

```bash
make build          # 本机
make linux-amd64    # 交叉编译
make linux-arm64
make test race vet  # 测试 / 竞态 / 静态检查
```

发布：

```bash
git tag v1.1.0
git push origin v1.1.0
```

GitHub Actions 依次执行测试 → 6 平台构建 → 打包 ZIP/DEB/RPM → 产物校验 → 创建 Release。
Release 说明自动汇总上个 Tag 以来的提交；发行包统一命名为 `netgw-<版本>-<平台>-<架构>.<扩展名>`。

## systemd 手动部署

```bash
sudo cp netgw-<版本>-linux-amd64 /usr/local/bin/netgw
sudo mkdir -p /etc/netgw && sudo cp config.yaml /etc/netgw/
sudo cp deploy/netgw.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now netgw
```

## 项目结构

```text
netgw/
├── cmd/gateway/      # 入口：配置加载、日志、信号处理
├── internal/
│   ├── config/       # 配置结构与 YAML 子集解析器
│   ├── gateway/      # 监听、TLS 嗅探、优雅退出
│   └── proxy/        # 前缀路由 + ReverseProxy
├── deploy/           # systemd 服务文件
├── scripts/          # 构建、打包、产物校验
├── config.yaml
└── Makefile
```

## 故障排查

| 现象 | 原因 / 处理 |
| --- | --- |
| `bind: address already in use` | 端口被占用：`ss -ltnp \| grep 8000`，或修改 `server.listen` |
| 返回 502 | 后端未启动或不可达，检查 `backend` 与 `routes.http` |
| HTTPS 无法访问 | 确认证书/私钥均已配置且服务账号可读、证书未过期 |
| `bind: permission denied` | 端口 < 1024 需 `CAP_NET_BIND_SERVICE` 或改用高端口 |

日志通过 `log/slog` 输出到 stderr，不记录请求体等敏感数据。

## 开源协议

[MIT License](./LICENSE)。发行包均附带完整协议文本，DEB/RPM 安装后位于 `/usr/share/doc/netgw/copyright`。

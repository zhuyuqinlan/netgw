#!/usr/bin/env bash
# 本地开发运行：读取当前目录 config.local.yaml，不存在时自动生成
# 默认模板（与发行包 config.yaml 一致的全注释示例）。
# 用法：./scripts/run-local.sh [gen]   gen 仅生成配置不启动
set -euo pipefail

cd "$(dirname "$0")/.."

CONFIG=config.local.yaml

if [ ! -f "$CONFIG" ]; then
    echo "==> $CONFIG 不存在，从 config.yaml 模板生成"
    cp config.yaml "$CONFIG"
    echo "==> 已生成 $CONFIG，按需取消注释后重新运行"
fi

if [ "${1:-}" = "gen" ]; then
    exit 0
fi

echo "==> 构建并启动（配置：$CONFIG）"
go build -o /tmp/netgw-dev ./cmd/gateway
exec /tmp/netgw-dev -config "$CONFIG"

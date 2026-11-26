#!/usr/bin/env bash
# pack-zip.sh 生成便携式 ZIP 发行包。
#
# 用法: pack-zip.sh <os> <arch> <version> <binfile> <outfile>
#   os:      windows | linux | darwin
#   arch:    amd64 | arm64
#   binfile: 已构建的可执行文件路径
#   outfile: 输出 zip 路径，如 dist/tcpgw-linux-amd64.zip
#
# ZIP 结构（顶层目录统一为 tcpgw/）:
#   tcpgw/
#     tcpgw[.exe]
#     README.md
#     LICENSE
#     config.yaml     <- 默认配置模板，不包含用户实际配置
set -euo pipefail

os="$1"; arch="$2"; version="$3"; binfile="$4"; outfile="$5"

die() { echo "pack-zip: ERROR: $*" >&2; exit 1; }

[ -f "$binfile" ] || die "binary not found: $binfile"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/tcpgw"
if [ "$os" = "windows" ]; then
  exe="tcpgw.exe"
else
  exe="tcpgw"
fi

cp "$binfile" "$work/tcpgw/$exe"
cp "$REPO_ROOT/README.md" "$REPO_ROOT/LICENSE" "$work/tcpgw/"
# 统一使用仓库根目录的默认配置模板（不含用户实际配置）。
cp "$REPO_ROOT/config.yaml" "$work/tcpgw/config.yaml"

[ -f "$work/tcpgw/README.md" ] || die "README.md missing"
[ -f "$work/tcpgw/LICENSE" ] || die "LICENSE missing"

# Linux/macOS 保留可执行权限；zip 本身记录权限位。
if [ "$os" != "windows" ]; then
  chmod 755 "$work/tcpgw/$exe"
fi

mkdir -p "$(dirname "$outfile")"
outfile_abs="$(cd "$(dirname "$outfile")" && pwd)/$(basename "$outfile")"

# 压缩：优先 zip（正确记录 Unix 权限位），其次 Python zipfile（显式写 755），最后 7z。
if command -v zip >/dev/null 2>&1; then
  (cd "$work" && zip -r -X -q "$outfile_abs" tcpgw) || die "zip failed"
elif command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1; then
  PY="$(command -v python3 || command -v python)"
  (cd "$work" && "$PY" - "$outfile_abs" "$os" <<'PYEOF'
import os, stat, sys, zipfile
outfile, os_name = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(outfile, "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk("tcpgw"):
        for name in dirs + files:
            p = os.path.join(root, name)
            arc = p.replace(os.sep, "/")
            is_exe = (root == "tcpgw" and name == "tcpgw" and os_name != "windows")
            mode = 0o755 if (os.path.isdir(p) or is_exe) else 0o644
            if os.path.isdir(p):
                zi = zipfile.ZipInfo(arc + "/")
                zi.external_attr = (0o755 << 16) | 0x10
                z.writestr(zi, "")
            else:
                zi = zipfile.ZipInfo(arc)
                zi.external_attr = mode << 16
                with open(p, "rb") as f:
                    z.writestr(zi, f.read())
PYEOF
) || die "python zip failed"
elif command -v 7z >/dev/null 2>&1; then
  (cd "$work" && 7z a -tzip "$outfile_abs" tcpgw >/dev/null) || die "7z failed"
else
  die "no zip tool found (need zip or 7z)"
fi

echo "pack-zip: created $outfile (os=$os arch=$arch version=$version)"

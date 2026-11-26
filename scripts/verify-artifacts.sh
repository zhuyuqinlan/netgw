#!/usr/bin/env bash
# verify-artifacts.sh 校验 dist/ 下的构建产物。
#
# 检查项：
#   - 10 个 ZIP / DEB / RPM 文件存在且非空
#   - ZIP 内部目录结构：tcpgw/ 下含可执行文件、README.md、LICENSE、config.yaml
#   - Linux/macOS 二进制权限（zipinfo 权限位）
#   - DEB：架构元数据、conffiles、systemd service、无 /usr/bin 条目
#   - RPM：架构标签、systemd service、noreplace 配置
#   - Linux 二进制真实架构（file / readelf）
set -euo pipefail

die() { echo "verify: FAIL: $*" >&2; exit 1; }
ok()  { echo "verify: ok: $*"; }

DIST="${1:-dist}"

fail=0
check() {
  if "$@" >/dev/null 2>&1; then return 0; else return 1; fi
}

# ---- 1. 产物存在 ----
for f in \
  tcpgw-windows-amd64.zip tcpgw-windows-arm64.zip \
  tcpgw-linux-amd64.zip tcpgw-linux-arm64.zip \
  tcpgw-linux-amd64.deb tcpgw-linux-arm64.deb \
  tcpgw-linux-amd64.rpm tcpgw-linux-arm64.rpm \
  tcpgw-darwin-amd64.zip tcpgw-darwin-arm64.zip; do
  p="$DIST/$f"
  [ -s "$p" ] || { echo "verify: FAIL: missing or empty: $p" >&2; fail=1; }
done
[ "$fail" = 0 ] || die "artifact files incomplete"

# ---- 2. ZIP 结构 ----
for f in tcpgw-windows-amd64.zip tcpgw-windows-arm64.zip \
         tcpgw-linux-amd64.zip tcpgw-linux-arm64.zip \
         tcpgw-darwin-amd64.zip tcpgw-darwin-arm64.zip; do
  p="$DIST/$f"
  listing="$(unzip -l "$p")"
  for entry in tcpgw/README.md tcpgw/LICENSE tcpgw/config.yaml; do
    echo "$listing" | grep -q " $entry\$" || { echo "verify: FAIL: $f missing $entry" >&2; fail=1; }
  done
  echo "$listing" | grep -qE "tcpgw/tcpgw(\.exe)?\$" || { echo "verify: FAIL: $f missing executable" >&2; fail=1; }
  # 不允许 .git / 源码 / 测试文件混入。
  echo "$listing" | grep -qE "\.git|_test\.go|\.go$" && { echo "verify: FAIL: $f contains sources" >&2; fail=1; }

  # 非 Windows ZIP：可执行权限 755。
  if echo "$f" | grep -qE "linux|darwin"; then
    perms="$(zipinfo -l "$p" | awk "/tcpgw\\/tcpgw\$/ {print \$1}")"
    case "$perms" in
      *rwxr-xr-x*) ok "$f exec permission 755" ;;
      *) echo "verify: FAIL: $f exec permission = $perms" >&2; fail=1 ;;
    esac
  fi
done

# ---- 3. Linux 二进制真实架构 ----
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
arch_check() {
  local z="$1"; shift
  unzip -q -o "$DIST/$z" tcpgw/tcpgw -d "$tmp/$z" 2>/dev/null || return 1
  local got
  got="$(file "$tmp/$z/tcpgw/tcpgw")"
  local w
  for w in "$@"; do
    if grep -qiE "$w" <<<"$got"; then
      ok "$z binary arch $w"
      return 0
    fi
  done
  echo "verify: FAIL: $z arch mismatch: $got" >&2
  return 1
}
arch_check tcpgw-linux-amd64.zip "x86[-_]64" || fail=1
arch_check tcpgw-linux-arm64.zip "aarch64" || fail=1
arch_check tcpgw-darwin-amd64.zip "x86[-_]64" || fail=1
arch_check tcpgw-darwin-arm64.zip "arm64" || fail=1

# ---- 4. DEB ----
for a in amd64 arm64; do
  p="$DIST/tcpgw-linux-$a.deb"
  arch_meta="$(dpkg-deb -f "$p" Architecture)"
  want=$a
  [ "$arch_meta" = "$want" ] || { echo "verify: FAIL: $p Architecture=$arch_meta" >&2; fail=1; }
  listing="$(dpkg-deb -c "$p")"
  echo "$listing" | grep -q "usr/lib/tcpgw/tcpgw" || { echo "verify: FAIL: $p missing binary" >&2; fail=1; }
  echo "$listing" | grep -q "usr/share/doc/tcpgw/copyright" || { echo "verify: FAIL: $p missing copyright" >&2; fail=1; }
  echo "$listing" | grep -q "usr/lib/systemd/system/tcpgw.service" || { echo "verify: FAIL: $p missing service" >&2; fail=1; }
  echo "$listing" | grep -qE "usr/bin|usr/local" && { echo "verify: FAIL: $p installs to /usr/bin" >&2; fail=1; }
  echo "$listing" | grep -q "etc/tcpgw/config.yaml" || { echo "verify: FAIL: $p missing config" >&2; fail=1; }
  # 兼容不同 dpkg-deb 版本：优先 --control-show，回退到解包 control.tar。
  conffiles="$(dpkg-deb --control-show "$p" ./conffiles 2>/dev/null \
    || dpkg-deb --fsys-tarfile "$p" 2>/dev/null | tar -xOzf - ./conffiles 2>/dev/null \
    || dpkg-deb --info "$p" conffiles 2>/dev/null || true)"
  echo "$conffiles" | grep -q "/etc/tcpgw/config.yaml" || { echo "verify: FAIL: $p conffiles missing" >&2; fail=1; }
  ok "deb $a checks passed"
done

# ---- 5. RPM ----
for a in x86_64 aarch64; do
  if [ "$a" = x86_64 ]; then p="$DIST/tcpgw-linux-amd64.rpm"; else p="$DIST/tcpgw-linux-arm64.rpm"; fi
  arch_meta="$(rpm -qp --qf '%{ARCH}' "$p" 2>/dev/null || true)"
  [ "$arch_meta" = "$a" ] || { echo "verify: FAIL: $p ARCH=$arch_meta" >&2; fail=1; }
  listing="$(rpm -qlp "$p" 2>/dev/null)"
  echo "$listing" | grep -q "/usr/lib/tcpgw/tcpgw" || { echo "verify: FAIL: $p missing binary" >&2; fail=1; }
  echo "$listing" | grep -q "/usr/lib/systemd/system/tcpgw.service" || { echo "verify: FAIL: $p missing service" >&2; fail=1; }
  echo "$listing" | grep -qE "/usr/bin|/usr/local" && { echo "verify: FAIL: $p installs to /usr/bin" >&2; fail=1; }
  noreplace="$(rpm -qp --qf '[%{FILENAMES} %{FILEFLAGS}\n]' "$p" 2>/dev/null | grep 'etc/tcpgw/config.yaml' | awk '{print $2}')"
  # RPMFILE_CONFIG=1<<0，RPMFILE_NOREPLACE=1<<4；两者都置位才是 noreplace 配置。
  [ $(( noreplace & 17 )) -eq 17 ] 2>/dev/null || { echo "verify: FAIL: $p config not noreplace" >&2; fail=1; }
  ok "rpm $a checks passed"
done

[ "$fail" = 0 ] || die "verification failed"
echo "verify: ALL ARTIFACT CHECKS PASSED"

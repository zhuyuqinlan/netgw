#!/usr/bin/env bash
# verify-artifacts.sh 校验 dist/ 下的构建产物。
#
# 用法: verify-artifacts.sh <dist-dir> <version>
#   version: 不带 v 前缀的版本号，如 1.2.0；产物名为 netgw-<version>-<os>-<arch>.<ext>
#
# 检查项：
#   - 10 个 ZIP / DEB / RPM 文件存在且非空
#   - 文件名包含版本号；DEB/RPM 包元数据版本号一致
#   - ZIP 内部目录结构：netgw/ 下含可执行文件、README.md、LICENSE、config.yaml
#   - Linux/macOS 二进制权限（zipinfo 权限位）
#   - DEB：架构元数据、conffiles、systemd service、无 /usr/bin 条目
#   - RPM：架构标签、systemd service、noreplace 配置
#   - Linux 二进制真实架构（file / readelf）
set -euo pipefail

die() { echo "verify: FAIL: $*" >&2; exit 1; }
ok()  { echo "verify: ok: $*"; }

DIST="${1:-dist}"
VERSION="${2:-}"
[ -n "$VERSION" ] || die "usage: verify-artifacts.sh <dist-dir> <version>"
VERSION="${VERSION#v}"

fail=0
zip_name() { echo "netgw-$VERSION-$1.zip"; }
deb_name() { echo "netgw-$VERSION-linux-$1.deb"; }
rpm_name() { echo "netgw-$VERSION-linux-$1.rpm"; }

# ---- 1. 产物存在 ----
for f in \
  "$(zip_name windows-amd64)" "$(zip_name windows-arm64)" \
  "$(zip_name linux-amd64)" "$(zip_name linux-arm64)" \
  "$(deb_name amd64)" "$(deb_name arm64)" \
  "$(rpm_name amd64)" "$(rpm_name arm64)" \
  "$(zip_name darwin-amd64)" "$(zip_name darwin-arm64)"; do
  p="$DIST/$f"
  [ -s "$p" ] || { echo "verify: FAIL: missing or empty: $p" >&2; fail=1; }
done
[ "$fail" = 0 ] || die "artifact files incomplete"

# ---- 2. ZIP 结构 ----
for f in "$(zip_name windows-amd64)" "$(zip_name windows-arm64)" \
         "$(zip_name linux-amd64)" "$(zip_name linux-arm64)" \
         "$(zip_name darwin-amd64)" "$(zip_name darwin-arm64)"; do
  p="$DIST/$f"
  listing="$(unzip -l "$p")"
  for entry in netgw/README.md netgw/LICENSE netgw/config.yaml; do
    echo "$listing" | grep -q " $entry\$" || { echo "verify: FAIL: $f missing $entry" >&2; fail=1; }
  done
  echo "$listing" | grep -qE "netgw/netgw(\.exe)?\$" || { echo "verify: FAIL: $f missing executable" >&2; fail=1; }
  # 不允许 .git / 源码 / 测试文件混入。
  echo "$listing" | grep -qE "\.git|_test\.go|\.go$" && { echo "verify: FAIL: $f contains sources" >&2; fail=1; }

  # 非 Windows ZIP：可执行权限 755。
  if echo "$f" | grep -qE "linux|darwin"; then
    perms="$(zipinfo -l "$p" | awk "/netgw\\/netgw\$/ {print \$1}")"
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
  unzip -q -o "$DIST/$z" netgw/netgw -d "$tmp/$z" 2>/dev/null || return 1
  local got
  got="$(file "$tmp/$z/netgw/netgw")"
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
arch_check "$(zip_name linux-amd64)" "x86[-_]64" || fail=1
arch_check "$(zip_name linux-arm64)" "aarch64" || fail=1
arch_check "$(zip_name darwin-amd64)" "x86[-_]64" || fail=1
arch_check "$(zip_name darwin-arm64)" "arm64" || fail=1

# ---- 4. DEB ----
for a in amd64 arm64; do
  p="$DIST/$(deb_name "$a")"
  arch_meta="$(dpkg-deb -f "$p" Architecture)"
  [ "$arch_meta" = "$a" ] || { echo "verify: FAIL: $p Architecture=$arch_meta" >&2; fail=1; }
  ver_meta="$(dpkg-deb -f "$p" Version)"
  [ "$ver_meta" = "$VERSION" ] || { echo "verify: FAIL: $p Version=$ver_meta want $VERSION" >&2; fail=1; }
  listing="$(dpkg-deb -c "$p")"
  echo "$listing" | grep -q "usr/lib/netgw/netgw" || { echo "verify: FAIL: $p missing binary" >&2; fail=1; }
  echo "$listing" | grep -q "usr/share/doc/netgw/copyright" || { echo "verify: FAIL: $p missing copyright" >&2; fail=1; }
  echo "$listing" | grep -q "usr/lib/systemd/system/netgw.service" || { echo "verify: FAIL: $p missing service" >&2; fail=1; }
  echo "$listing" | grep -qE "usr/bin|usr/local" && { echo "verify: FAIL: $p installs to /usr/bin" >&2; fail=1; }
  echo "$listing" | grep -q "etc/netgw/config.yaml" || { echo "verify: FAIL: $p missing config" >&2; fail=1; }
  # 兼容不同 dpkg-deb 版本：优先 --control-show，回退到解包 control.tar。
  conffiles="$(dpkg-deb --control-show "$p" ./conffiles 2>/dev/null \
    || dpkg-deb --fsys-tarfile "$p" 2>/dev/null | tar -xOzf - ./conffiles 2>/dev/null \
    || dpkg-deb --info "$p" conffiles 2>/dev/null || true)"
  echo "$conffiles" | grep -q "/etc/netgw/config.yaml" || { echo "verify: FAIL: $p conffiles missing" >&2; fail=1; }
  ok "deb $a checks passed (version $ver_meta)"
done

# ---- 5. RPM ----
for a in x86_64 aarch64; do
  if [ "$a" = x86_64 ]; then p="$DIST/$(rpm_name amd64)"; else p="$DIST/$(rpm_name arm64)"; fi
  arch_meta="$(rpm -qp --qf '%{ARCH}' "$p" 2>/dev/null || true)"
  [ "$arch_meta" = "$a" ] || { echo "verify: FAIL: $p ARCH=$arch_meta" >&2; fail=1; }
  ver_meta="$(rpm -qp --qf '%{VERSION}' "$p" 2>/dev/null || true)"
  [ "$ver_meta" = "$VERSION" ] || { echo "verify: FAIL: $p VERSION=$ver_meta want $VERSION" >&2; fail=1; }
  listing="$(rpm -qlp "$p" 2>/dev/null)"
  echo "$listing" | grep -q "/usr/lib/netgw/netgw" || { echo "verify: FAIL: $p missing binary" >&2; fail=1; }
  echo "$listing" | grep -q "/usr/lib/systemd/system/netgw.service" || { echo "verify: FAIL: $p missing service" >&2; fail=1; }
  echo "$listing" | grep -qE "/usr/bin|/usr/local" && { echo "verify: FAIL: $p installs to /usr/bin" >&2; fail=1; }
  noreplace="$(rpm -qp --qf '[%{FILENAMES} %{FILEFLAGS}\n]' "$p" 2>/dev/null | grep 'etc/netgw/config.yaml' | awk '{print $2}')"
  # RPMFILE_CONFIG=1<<0，RPMFILE_NOREPLACE=1<<4；两者都置位才是 noreplace 配置。
  [ $(( noreplace & 17 )) -eq 17 ] 2>/dev/null || { echo "verify: FAIL: $p config not noreplace" >&2; fail=1; }
  ok "rpm $a checks passed (version $ver_meta)"
done

[ "$fail" = 0 ] || die "verification failed"
echo "verify: ALL ARTIFACT CHECKS PASSED"

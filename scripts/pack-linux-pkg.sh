#!/usr/bin/env bash
# pack-linux-pkg.sh 生成 Linux DEB/RPM 包。
#
# 用法: pack-linux-pkg.sh <deb|rpm> <arch> <version> <binfile> <outfile>
#   arch: amd64 | arm64
#
# 安装布局:
#   /usr/sbin/netgw
#   /etc/netgw/config.yaml          <- conffile/noreplace，升级不覆盖
#   /usr/share/doc/netgw/README.md
#   /usr/share/doc/netgw/copyright  (MIT)
#   /usr/lib/systemd/system/netgw.service
#
# 仅 DEB/RPM 安装注册 systemd 服务；不创建 /usr/bin 软链接、不注册 shell 命令。
set -euo pipefail

kind="$1"; arch="$2"; version="$3"; binfile="$4"; outfile="$5"

die() { echo "pack-linux-pkg: ERROR: $*" >&2; exit 1; }

[ -f "$binfile" ] || die "binary not found: $binfile"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE="$REPO_ROOT/deploy/netgw.service"
[ -f "$SERVICE" ] || die "service file missing: $SERVICE"

case "$arch" in
  amd64) deb_arch=amd64; rpm_arch=x86_64 ;;
  arm64) deb_arch=arm64; rpm_arch=aarch64 ;;
  *) die "unsupported arch: $arch" ;;
esac

# 去掉版本号里的 v 前缀。
pkg_version="${version#v}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pkgroot="$work/pkg"
install -Dm755 "$binfile"          "$pkgroot/usr/sbin/netgw"
install -Dm644 "$REPO_ROOT/config.yaml" "$pkgroot/etc/netgw/config.yaml"
install -Dm644 "$REPO_ROOT/README.md"   "$pkgroot/usr/share/doc/netgw/README.md"
install -Dm644 "$REPO_ROOT/LICENSE"     "$pkgroot/usr/share/doc/netgw/copyright"
install -Dm644 "$SERVICE"               "$pkgroot/usr/lib/systemd/system/netgw.service"

mkdir -p "$(dirname "$outfile")"

if [ "$kind" = "deb" ]; then
  # ---- DEB (dpkg-deb) ----
  ctrl="$pkgroot/DEBIAN"
  mkdir -p "$ctrl"

  # conffiles：升级时 dpkg 保留用户修改（询问或保留），不覆盖配置。
  echo "/etc/netgw/config.yaml" > "$ctrl/conffiles"

  cat > "$ctrl/control" <<EOF
Package: netgw
Version: $pkg_version
Architecture: $deb_arch
Maintainer: netgw maintainers <noreply@github.com>
Depends: libc6 | libc6-udeb
Section: net
Priority: optional
Homepage: https://github.com/zhuyuqinlan/netgw
Description: Lightweight HTTP(S) reverse proxy gateway
 TCP/HTTP(S) reverse proxy gateway with path-prefix routing,
 TLS termination and raw TCP passthrough on the same port.
EOF

  # postinst: 创建专用系统用户、安装后重载 systemd 并启用（不自动 start）。
  cat > "$ctrl/postinst" <<'EOF'
#!/bin/sh
set -e
# 专用非 root 系统用户（首次安装创建，已存在则跳过）。
if ! getent passwd netgw >/dev/null 2>&1; then
  adduser --system --group --no-create-home --home /nonexistent netgw 2>/dev/null \
    || useradd --system --no-create-home --home /nonexistent --shell /usr/sbin/nologin netgw 2>/dev/null || true
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable netgw.service || true
fi
exit 0
EOF

  # prerm: 卸载/升级前停止并禁用服务。
  cat > "$ctrl/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "remove" ] || [ "$1" = "upgrade" ]; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop netgw.service || true
    systemctl disable netgw.service || true
  fi
fi
exit 0
EOF

  # postrm: 仅在 purge（用户显式要求清除）时删除配置；默认 remove 保留配置。
  cat > "$ctrl/postrm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "purge" ]; then
  rm -f /etc/netgw/config.yaml
  rmdir /etc/netgw 2>/dev/null || true
  if getent passwd netgw >/dev/null 2>&1; then
    userdel netgw 2>/dev/null || true
  fi
fi
exit 0
EOF
  chmod 755 "$ctrl/postinst" "$ctrl/prerm" "$ctrl/postrm"

  dpkg-deb --root-owner-group --build "$pkgroot" "$outfile.tmp"
  mv "$outfile.tmp" "$outfile"
  echo "pack-linux-pkg: created $outfile (deb $deb_arch)"

elif [ "$kind" = "rpm" ]; then
  # ---- RPM (rpmbuild) ----
  rpmroot="$work/rpmbuild"
  mkdir -p "$rpmroot/BUILD" "$rpmroot/RPMS" "$rpmroot/SPECS"

  cat > "$rpmroot/SPECS/netgw.spec" <<EOF
Name:           netgw
Version:        $pkg_version
Release:        1%{?dist}
Summary:        Lightweight HTTP(S) reverse proxy gateway
License:        MIT
URL:            https://github.com/zhuyuqinlan/netgw
# 注意：spec 里不写 BuildArch，由 rpmbuild --target 决定架构，
# 否则在异构宿主上交叉构建会报 No compatible architectures found for build。
Requires:       systemd

%description
TCP/HTTP(S) reverse proxy gateway with path-prefix routing,
TLS termination and raw TCP passthrough on the same port.

%prep
%build
%install
mkdir -p %{buildroot}/usr/sbin %{buildroot}/etc/netgw \
         %{buildroot}/usr/share/doc/netgw %{buildroot}/usr/lib/systemd/system
install -m755 "$pkgroot/usr/sbin/netgw"                   %{buildroot}/usr/sbin/netgw
install -m644 "$pkgroot/etc/netgw/config.yaml"            %{buildroot}/etc/netgw/config.yaml
install -m644 "$pkgroot/usr/share/doc/netgw/README.md"    %{buildroot}/usr/share/doc/netgw/README.md
install -m644 "$pkgroot/usr/share/doc/netgw/copyright"    %{buildroot}/usr/share/doc/netgw/copyright
install -m644 "$pkgroot/usr/lib/systemd/system/netgw.service" %{buildroot}/usr/lib/systemd/system/netgw.service

%files
/usr/sbin/netgw
%config(noreplace) /etc/netgw/config.yaml
/usr/share/doc/netgw/README.md
/usr/share/doc/netgw/copyright
/usr/lib/systemd/system/netgw.service

# 说明：脚本内联 systemd 操作，不依赖构建机上的 systemd-rpm-macros。
# （CI 在 Ubuntu 上构建时不会展开相关宏，宏原文会写进脚本导致安装失败。）
%post
getent passwd netgw >/dev/null 2>&1 || \\
  useradd --system --no-create-home --home /nonexistent --shell /sbin/nologin netgw 2>/dev/null || true
if [ "\$1" -eq 1 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl --no-reload enable netgw.service >/dev/null 2>&1 || true
  systemctl daemon-reload >/dev/null 2>&1 || true
fi

%preun
if [ "\$1" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl --no-reload disable --now netgw.service >/dev/null 2>&1 || true
fi

%postun
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
fi
# 配置文件默认保留，仅在显式卸载并自行删除 /etc/netgw 时清除。
EOF

  # --target：允许在任意宿主架构上交叉构建（如 x86_64 宿主打 aarch64 包）。
  rpmbuild --define "_topdir $rpmroot" --define "_rpmdir $(dirname "$outfile")" \
    --target "$rpm_arch" -bb "$rpmroot/SPECS/netgw.spec"
  # rpmbuild 输出到 <outdir>/<arch>/netgw-<ver>.<arch>.rpm，移动到目标名。
  built="$(find "$(dirname "$outfile")" -name "netgw-$pkg_version-1*.rpm" -newer "$binfile" ! -path "*/netgw.spec*" | head -1)"
  [ -n "$built" ] || built="$(find "$(dirname "$outfile")" -name "netgw-*.rpm" | head -1)"
  [ -n "$built" ] || die "rpmbuild produced no rpm"
  if [ "$built" != "$outfile" ]; then
    mv "$built" "$outfile"
  fi
  echo "pack-linux-pkg: created $outfile (rpm $rpm_arch)"
else
  die "unsupported package kind: $kind (use deb|rpm)"
fi

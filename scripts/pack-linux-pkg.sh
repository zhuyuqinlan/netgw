#!/usr/bin/env bash
# pack-linux-pkg.sh 生成 Linux DEB/RPM 包。
#
# 用法: pack-linux-pkg.sh <deb|rpm> <arch> <version> <binfile> <outfile>
#   arch: amd64 | arm64
#
# 安装布局:
#   /usr/lib/tcpgw/tcpgw
#   /etc/tcpgw/config.yaml          <- conffile/noreplace，升级不覆盖
#   /usr/share/doc/tcpgw/README.md
#   /usr/share/doc/tcpgw/copyright  (MIT)
#   /usr/lib/systemd/system/tcpgw.service
#
# 仅 DEB/RPM 安装注册 systemd 服务；不创建 /usr/bin 软链接、不注册 shell 命令。
set -euo pipefail

kind="$1"; arch="$2"; version="$3"; binfile="$4"; outfile="$5"

die() { echo "pack-linux-pkg: ERROR: $*" >&2; exit 1; }

[ -f "$binfile" ] || die "binary not found: $binfile"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE="$REPO_ROOT/deploy/tcpgw.service"
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
install -Dm755 "$binfile"          "$pkgroot/usr/lib/tcpgw/tcpgw"
install -Dm644 "$REPO_ROOT/config.yaml" "$pkgroot/etc/tcpgw/config.yaml"
install -Dm644 "$REPO_ROOT/README.md"   "$pkgroot/usr/share/doc/tcpgw/README.md"
install -Dm644 "$REPO_ROOT/LICENSE"     "$pkgroot/usr/share/doc/tcpgw/copyright"
install -Dm644 "$SERVICE"               "$pkgroot/usr/lib/systemd/system/tcpgw.service"

mkdir -p "$(dirname "$outfile")"

if [ "$kind" = "deb" ]; then
  # ---- DEB (dpkg-deb) ----
  ctrl="$work/DEBIAN"
  mkdir -p "$ctrl"

  # conffiles：升级时 dpkg 保留用户修改（询问或保留），不覆盖配置。
  echo "/etc/tcpgw/config.yaml" > "$ctrl/conffiles"

  cat > "$ctrl/control" <<EOF
Package: tcpgw
Version: $pkg_version
Architecture: $deb_arch
Maintainer: tcp-protocol-gateway maintainers <noreply@github.com>
Depends: libc6 | libc6-udeb
Section: net
Priority: optional
Homepage: https://github.com/zhuyuqin/tcp-protocol-gateway
Description: Lightweight HTTP(S) reverse proxy gateway
 TCP/HTTP(S) reverse proxy gateway with path-prefix routing,
 TLS termination and raw TCP passthrough on the same port.
EOF

  # postinst: 创建专用系统用户、安装后重载 systemd 并启用（不自动 start）。
  cat > "$ctrl/postinst" <<'EOF'
#!/bin/sh
set -e
# 专用非 root 系统用户（首次安装创建，已存在则跳过）。
if ! getent passwd tcpgw >/dev/null 2>&1; then
  adduser --system --group --no-create-home --home /nonexistent tcpgw 2>/dev/null \
    || useradd --system --no-create-home --home /nonexistent --shell /usr/sbin/nologin tcpgw 2>/dev/null || true
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable tcpgw.service || true
fi
exit 0
EOF

  # prerm: 卸载/升级前停止并禁用服务。
  cat > "$ctrl/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "remove" ] || [ "$1" = "upgrade" ]; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop tcpgw.service || true
    systemctl disable tcpgw.service || true
  fi
fi
exit 0
EOF

  # postrm: 仅在 purge（用户显式要求清除）时删除配置；默认 remove 保留配置。
  cat > "$ctrl/postrm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = "purge" ]; then
  rm -f /etc/tcpgw/config.yaml
  rmdir /etc/tcpgw 2>/dev/null || true
  if getent passwd tcpgw >/dev/null 2>&1; then
    userdel tcpgw 2>/dev/null || true
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

  cat > "$rpmroot/SPECS/tcpgw.spec" <<EOF
Name:           tcpgw
Version:        $pkg_version
Release:        1%{?dist}
Summary:        Lightweight HTTP(S) reverse proxy gateway
License:        MIT
URL:            https://github.com/zhuyuqin/tcp-protocol-gateway
BuildArch:      $rpm_arch
Requires:       systemd

%description
TCP/HTTP(S) reverse proxy gateway with path-prefix routing,
TLS termination and raw TCP passthrough on the same port.

%prep
%build
%install
mkdir -p %{buildroot}/usr/lib/tcpgw %{buildroot}/etc/tcpgw \
         %{buildroot}/usr/share/doc/tcpgw %{buildroot}/usr/lib/systemd/system
install -m755 $(find "$pkgroot/usr/lib/tcpgw" -name tcpgw) %{buildroot}/usr/lib/tcpgw/tcpgw
install -m644 $(find "$pkgroot/etc/tcpgw" -name config.yaml) %{buildroot}/etc/tcpgw/config.yaml
install -m644 $(find "$pkgroot/usr/share/doc/tcpgw" -name README.md) %{buildroot}/usr/share/doc/tcpgw/README.md
install -m644 $(find "$pkgroot/usr/share/doc/tcpgw" -name copyright) %{buildroot}/usr/share/doc/tcpgw/copyright
install -m644 $(find "$pkgroot/usr/lib/systemd/system" -name tcpgw.service) %{buildroot}/usr/lib/systemd/system/tcpgw.service

%files
/usr/lib/tcpgw/tcpgw
%config(noreplace) /etc/tcpgw/config.yaml
/usr/share/doc/tcpgw/README.md
/usr/share/doc/tcpgw/copyright
/usr/lib/systemd/system/tcpgw.service

%post
getent passwd tcpgw >/dev/null 2>&1 || \\
  useradd --system --no-create-home --home /nonexistent --shell /sbin/nologin tcpgw 2>/dev/null || true
%systemd_post tcpgw.service

%preun
%systemd_preun tcpgw.service

%postun
%systemd_postun_with_restart tcpgw.service
# 配置文件默认保留，仅在 rpm -e --nodeps 之外的显式卸载场景由用户决定。
EOF

  rpmbuild --define "_topdir $rpmroot" --define "_rpmdir $(dirname "$outfile")" \
    -bb "$rpmroot/SPECS/tcpgw.spec"
  # rpmbuild 输出到 <outdir>/<arch>/tcpgw-<ver>.<arch>.rpm，移动到目标名。
  built="$(find "$(dirname "$outfile")" -name "tcpgw-$pkg_version-1*.rpm" -newer "$binfile" | head -1)"
  [ -n "$built" ] || built="$(find "$(dirname "$outfile")" -name "tcpgw-*.rpm" | head -1)"
  [ -n "$built" ] || die "rpmbuild produced no rpm"
  if [ "$built" != "$outfile" ]; then
    mv "$built" "$outfile"
  fi
  echo "pack-linux-pkg: created $outfile (rpm $rpm_arch)"
else
  die "unsupported package kind: $kind (use deb|rpm)"
fi

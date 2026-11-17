#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
engine=$(realpath "${1:-$repo/dist/engine}")
version=${2:-0.1.0-0015}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]]
arch=$(go env GOARCH)
case "$arch" in amd64) platform=x86_64; dsm_arch='bromolow x86_64'; machine='Advanced Micro Devices X86-64';; arm64) platform=armv8; dsm_arch=armv8; machine='AArch64';; *) exit 1;; esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
package="$work/payload"
outer="$work/spk"
mkdir -p "$package/bin/cli-plugins" "$package/ui" "$outer/scripts" "$repo/dist"
for name in dockerd docker-proxy docker containerd containerd-shim-runc-v2 ctr runc docker-init docker-compose; do
    executable="$engine/$name"
    readelf -h "$executable" | grep -Fq "$machine"
    if readelf -l "$executable" | grep -q 'Requesting program interpreter'; then
        printf '%s\n' "Dynamic binary rejected: $name" >&2
        exit 1
    fi
    install -m 0755 "$executable" "$package/bin/$name"
done
[[ -f "$engine/UPSTREAM.sources" && -d "$engine/licenses" ]]
cp "$engine/UPSTREAM.sources" "$package/"
cp -r "$engine/licenses" "$package/"
cp "$repo"/spk/ui/{config,style.css} "$package/ui/"
install -m 0644 "$repo/spk/ui/QiDocker.js" "$package/ui/QiDocker-0015.js"
ln -s ../docker-compose "$package/bin/cli-plugins/docker-compose"
(cd "$repo" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$package/bin/qidocker" ./cmd/qidocker)
ln -s ../bin/qidocker "$package/ui/control.cgi"
cat > "$package/bin/qidocker-cli" <<'CLI'
#!/bin/sh
set -eu
here=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
volume=${here%%/@appstore/*}
case "$volume" in /volume*) ;; *) exit 1 ;; esac
state="$volume/@QiDocker"
export DOCKER_HOST="unix://$state/run/docker.sock"
export DOCKER_CONFIG="$state/cli"
unset DOCKER_CONTEXT
export PATH="$here:$PATH"
if [ "${1:-}" = compose ]; then
    shift
    exec "$here/docker-compose" "$@"
fi
exec "$here/docker" "$@"
CLI
chmod 0755 "$package/bin/qidocker-cli"
for name in "$repo"/spk/scripts/*; do install -m 0755 "$name" "$outer/scripts/"; done
cp -r "$repo"/spk/{conf,WIZARD_UIFILES} "$outer/"
python3 "$repo/scripts/icons.py" "$outer" "$package/ui"
mkdir -p "$package/certs"
install -m 0644 "$repo/spk/certs/isrg-root-x1.pem" "$package/certs/isrg-root-x1.pem"
tar --format=ustar --owner=0 --group=0 -C "$package" -c bin ui certs licenses UPSTREAM.sources |
    xz -T2 -3 > "$outer/package.tgz"
cat > "$outer/INFO" <<INFO
package="QiDocker"
displayname="QiDocker"
version="$version"
arch="$dsm_arch"
os_min_ver="6.2-24922"
os_max_ver="6.2-25599"
description="QiStore Docker Engine development package for DSM 6.2"
maintainer="QiStore"
maintainer_url="https://github.com/QiStore"
dsmuidir="ui"
dsmappname="SYNO.SDS.QiDocker.Instance"
thirdparty="yes"
install_type="volume"
startable="yes"
ctl_stop="yes"
ctl_uninstall="yes"
silent_upgrade="yes"
extractsize="$(du -sk "$package" | cut -f1)"
checksum="$(md5sum "$outer/package.tgz" | cut -d ' ' -f1)"
INFO
spk="$repo/dist/QiDocker-$version-dsm62-$platform.spk"
# DSM expects INFO at the archive root, without a leading "./".
tar --format=ustar --owner=0 --group=0 -C "$outer" -cf "$spk" \
    INFO package.tgz scripts conf WIZARD_UIFILES PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG
tar -xOf "$spk" INFO | cmp "$outer/INFO" -
(cd "$repo/dist" && sha256sum "QiDocker-$version-dsm62-$platform.spk" > "QiDocker-$version-dsm62-$platform.spk.sha256")

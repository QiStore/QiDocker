#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
engine=$(realpath "${1:-$repo/dist/engine}")
version=${2:-0.1.0-0001}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$ ]]
arch=$(go env GOARCH)
case "$arch" in amd64) platform=x86_64; machine='Advanced Micro Devices X86-64';; arm64) platform=armv8; machine='AArch64';; *) exit 1;; esac
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
cp "$repo"/spk/ui/{config,QiDocker.js,style.css} "$package/ui/"
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
tar -C "$package" --owner=0 --group=0 -czf "$outer/package.tgz" .
cat > "$outer/INFO" <<INFO
package="QiDocker"
displayname="QiDocker"
version="$version"
arch="$platform"
firmware="6.2-24922"
os_max_ver="6.2.4-25599"
description="QiStore Docker Engine development package for DSM 6.2"
maintainer="QiStore"
maintainer_url="https://github.com/QiStore"
dsmuidir="ui"
dsmappname="SYNO.SDS.QiDocker.Instance"
startable="yes"
checksum="$(md5sum "$outer/package.tgz" | cut -d ' ' -f1)"
INFO
(cd "$outer" && tar --owner=0 --group=0 -cf "$repo/dist/QiDocker-$version-dsm62-$platform.spk" ./*)
(cd "$repo/dist" && sha256sum "QiDocker-$version-dsm62-$platform.spk" > "QiDocker-$version-dsm62-$platform.spk.sha256")

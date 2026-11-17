#!/usr/bin/env bash
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
out=$(mkdir -p "${1:-$repo/dist/engine}" && realpath "${1:-$repo/dist/engine}")
work=$(mktemp -d "${TMPDIR:-/tmp}/qidocker-build.XXXXXX")
trap 'rm -rf "$work"' EXIT
export GOTOOLCHAIN=local
export GOMODCACHE="$(go env GOMODCACHE)"
export GOPATH="$work/go"
mkdir -p "$GOPATH/src/github.com/docker" "$out/licenses"

checkout() {
    local upstream=$1 tag=$2 commit=$3 path=$4
    git -c advice.detachedHead=false clone --depth 1 --branch "$tag" "https://github.com/$upstream.git" "$path"
    [[ "$(git -C "$path" rev-parse HEAD)" == "$commit" ]]
    local name=${upstream##*/}
    printf '%s %s %s\n' "$upstream" "$tag" "$commit" >> "$out/UPSTREAM.sources"
    local entry
    for entry in LICENSE NOTICE AUTHORS; do
        if [[ -f "$path/$entry" ]]; then
            cp "$path/$entry" "$out/licenses/$name.$entry"
        fi
    done
}

: > "$out/UPSTREAM.sources"
moby="$GOPATH/src/github.com/docker/docker"
cli="$GOPATH/src/github.com/docker/cli"
checkout moby/moby v28.5.2 89c5e8fd66634b6128fc4c0e6f1236e2540e46e0 "$moby"
checkout docker/cli v28.5.2 ecc694264de6b34e4b59d16245603382f22fa813 "$cli"
checkout containerd/containerd v1.7.30 71c1c8666c6a999cc8c319160b6b2ea38c4a2c9e "$work/containerd"
checkout opencontainers/runc v1.3.6 491b69bab9fa206b984fb26ba07d3110d62e671f "$work/runc"
checkout docker/compose v2.40.3 49b1c1e932aecd3a4843b392162d4bd56021935e "$work/compose"
checkout krallin/tini v0.19.0 de40ad007797e0dcd8b7126f27bb87401d224240 "$work/tini"

pkg-config --exists libseccomp
(cd "$moby" && CGO_ENABLED=1 VERSION=28.5.2 DOCKER_GITCOMMIT=89c5e8f DOCKER_BUILDTAGS=seccomp ./hack/make.sh binary)
install "$moby/bundles/binary-daemon/"{dockerd,docker-proxy} "$out/"
(cd "$cli" && CGO_ENABLED=0 DISABLE_WARN_OUTSIDE_CONTAINER=1 make binary)
install "$cli/build/docker-linux-$(go env GOARCH)" "$out/docker"
CGO_ENABLED=0 make -C "$work/containerd" STATIC=1 binaries
install "$work/containerd/bin/"{containerd,containerd-shim-runc-v2,ctr} "$out/"
CGO_ENABLED=1 make -C "$work/runc" static
install "$work/runc/runc" "$out/"
(cd "$work/compose" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -X github.com/docker/compose/v2/internal.Version=v2.40.3' -o "$out/docker-compose" ./cmd)
mkdir -p "$work/tini-build"
sed -e 's/@tini_VERSION_MAJOR@/0/g' -e 's/@tini_VERSION_MINOR@/19/g' -e 's/@tini_VERSION_PATCH@/0/g' \
    -e 's/@tini_VERSION_GIT@/ - git.de40ad0/g' "$work/tini/src/tiniConfig.h.in" > "$work/tini-build/tiniConfig.h"
"${CC:-cc}" -I"$work/tini-build" -O2 -static -s -o "$out/docker-init" "$work/tini/src/tini.c"

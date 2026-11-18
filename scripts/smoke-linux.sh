#!/usr/bin/env bash
# Run only in a disposable Linux VM.
set -euo pipefail
[[ $# = 2 || $# = 3 ]]
[[ $EUID = 0 ]]
spk=$(realpath "$1")
report=$(realpath -m "$2")
existing_image=${3:-}
work=$(mktemp -d /tmp/qidocker-smoke.XXXXXX)
pid=
stop() {
    if [[ -n "$pid" ]]; then
        kill -TERM "$pid" 2>/dev/null || true
        wait "$pid" || true
        pid=
    fi
}
cleanup() {
    stop
    if [[ -f "$work/state/engine.log" ]]; then
        cp "$work/state/engine.log" "$report"
        chmod a+r "$report"
    fi
    rm -rf "$work"
}
trap cleanup EXIT
mkdir "$work/spk" "$work/target"
tar -xf "$spk" -C "$work/spk"
tar -xf "$work/spk/package.tgz" -C "$work/target"
bin="$work/target/bin"
export DOCKER_HOST="unix://$work/state/run/docker.sock"
export DOCKER_CONFIG="$work/docker-config"
unset DOCKER_CONTEXT
start() {
    "$bin/qidocker" serve "$work/state" "$work/target" &
    pid=$!
    local count
    for count in {1..30}; do
        if "$bin/docker" version --format '{{.Server.Version}}'; then return; fi
        kill -0 "$pid"
        sleep 1
    done
    return 1
}
start
if [[ -n "$existing_image" ]]; then "$bin/docker" load -i "$existing_image"; else "$bin/docker" pull hello-world:latest; fi
"$bin/docker" run --network none --name qidocker-hello hello-world:latest | grep -F 'Hello from Docker!'
"$bin/docker" rm qidocker-hello
"$bin/docker" save hello-world:latest -o "$work/hello-world.tar"
"$bin/docker" image rm hello-world:latest
"$bin/docker" load -i "$work/hello-world.tar"
stop
start
"$bin/docker" run --rm --network none hello-world:latest | grep -F 'Hello from Docker!'
"$bin/docker-compose" version
"$bin/runc" --version

# The offline smoke test can run with only a hello-world image archive.
if [[ -z "$existing_image" ]]; then
    "$bin/docker" pull nginx:alpine
    "$bin/docker" network create qidocker-smoke
    bridge="br-$("$bin/docker" network inspect -f '{{.Id}}' qidocker-smoke | cut -c1-12)"
    check_ports() {
        local name endpoint
        for name in qidocker-web-a qidocker-web-b; do
            endpoint=$("$bin/docker" port "$name" 80/tcp)
            curl -q --noproxy 127.0.0.1 --fail --silent --show-error \
                --retry 5 --retry-all-errors --max-time 3 "http://$endpoint/" >/dev/null
        done
        local flags
        for _ in {1..10}; do
            flags=$(cat "/sys/class/net/$bridge/flags")
            if (( flags & 256 )); then return; fi
            sleep 1
        done
        return 1
    }
    for name in qidocker-web-a qidocker-web-b; do
        "$bin/docker" run -d --restart always --name "$name" \
            --network qidocker-smoke -p 127.0.0.1::80 nginx:alpine
    done
    check_ports
    stop
    start
    check_ports
fi

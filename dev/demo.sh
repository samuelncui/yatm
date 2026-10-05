#!/usr/bin/env bash
set -euo pipefail

dev_mode=0
if [[ $# == 1 && $1 == --dev ]]; then
    dev_mode=1
elif [[ $# != 0 ]]; then
    echo "Usage: $0 [--dev]" >&2
    exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "${script_dir}/.." && pwd)
demo_root=${YATM_DEMO_ROOT:-"${TMPDIR:-/tmp}/yatm-demo"}
listen=${YATM_DEMO_LISTEN:-127.0.0.1:18080}
frontend_port=${YATM_DEMO_FRONTEND_PORT:-5173}
case "$demo_root" in /*) ;; *) demo_root="$PWD/$demo_root" ;; esac

if [[ $dev_mode == 1 ]]; then
    if [[ ! $frontend_port =~ ^[1-9][0-9]{0,4}$ ]] || (( frontend_port > 65535 )); then
        echo "Demo frontend port must be between 1 and 65535" >&2
        exit 2
    fi
fi

cd "${repo_root}"
prepare_args=(-root "${demo_root}" -listen "${listen}")
if [[ ${YATM_DEMO_RESET:-0} == 1 ]]; then
    prepare_args+=(-reset)
fi
if [[ -n ${YATM_DEMO_VIDEO:-} ]]; then
    prepare_args+=(-video "${YATM_DEMO_VIDEO}")
fi
if [[ ${YATM_DEMO_IDENTICAL_FILES+x} ]]; then
    prepare_args+=(-identical-files "${YATM_DEMO_IDENTICAL_FILES}")
fi
go run ./cmd/demo "${prepare_args[@]}"

if [[ $dev_mode == 0 ]]; then
    pnpm --dir frontend build
fi
go build -o "${demo_root}/yatm-httpd" ./cmd/httpd

if [[ $dev_mode == 1 ]]; then
    backend_pid=
    frontend_pid=
    cleanup() {
        trap - EXIT INT TERM
        for pid in "$backend_pid" "$frontend_pid"; do
            if [[ -n $pid ]]; then kill "$pid" 2>/dev/null || true; fi
        done
        for pid in "$backend_pid" "$frontend_pid"; do
            if [[ -n $pid ]]; then wait "$pid" 2>/dev/null || true; fi
        done
    }
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    (cd "${demo_root}" && exec ./yatm-httpd -config ./config.yaml) &
    backend_pid=$!
    (cd "${repo_root}/frontend" && DEV_SERVICE_BASE="http://${listen}" exec node ./node_modules/vite/bin/vite.js \
        --host localhost --port "$frontend_port" --strictPort) &
    frontend_pid=$!

    # Bash 3.2 has no wait -n. Stop both children when either one exits.
    while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
        sleep 0.2
    done
    if kill -0 "$backend_pid" 2>/dev/null; then
        wait "$frontend_pid"
    else
        wait "$backend_pid"
    fi
    exit
fi

rm -rf "${demo_root}/frontend"
mkdir -p "${demo_root}/frontend"
cp frontend/dist/index.html "${demo_root}/frontend/index.html"
cp -R frontend/dist/assets "${demo_root}/frontend/assets"

cd "${demo_root}"
exec ./yatm-httpd -config ./config.yaml

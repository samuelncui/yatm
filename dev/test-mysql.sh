#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname -- "${BASH_SOURCE[0]}")/.."
# Run the Go tests with every Catalog on MySQL. YATM_TEST_MYSQL_DSN names a disposable server
# without a database; each test creates and drops its own. Without it, a temporary local mysql:8.0
# container with the required utf8mb4_bin collation serves the run and is removed afterwards.
if [[ -z "${YATM_TEST_MYSQL_DSN:-}" ]]; then
  name="yatm-test-mysql-$$"
  docker run -d --rm --name "$name" -e MYSQL_ROOT_PASSWORD=yatmtest -p 127.0.0.1::3306 mysql:8.0 \
    --character-set-server=utf8mb4 --collation-server=utf8mb4_bin > /dev/null
  trap 'docker rm -f "$name" > /dev/null 2>&1 || true' EXIT
  # The image's initialization server accepts no TCP connections, so a TCP ping waits for the final one.
  until docker exec "$name" mysqladmin ping -h 127.0.0.1 -uroot -pyatmtest --silent > /dev/null 2>&1; do
    sleep 1
  done
  port=$(docker port "$name" 3306/tcp | head -n 1 | sed 's/.*://')
  export YATM_TEST_MYSQL_DSN="root:yatmtest@tcp(127.0.0.1:${port})/"
fi
go test -count=1 "${@:-./internal/...}"

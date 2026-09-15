#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
fusermount -u "${MOUNT_POINT}" 2>&1 | tee -a "${TAPE_DIR}/ltfs.log"
for _ in $(seq 1 100); do
    if ! mountpoint -q "${MOUNT_POINT}"; then
        exit 0
    fi
    sleep 0.05
done

echo "LTFS mount did not stop: ${MOUNT_POINT}" >&2
exit 1

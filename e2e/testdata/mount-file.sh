#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
ltfs -o tape_backend=file -o "devname=${DEVICE}" -o sync_type=unmount \
    -o "work_directory=${TAPE_DIR}" -o capture_index -s "${MOUNT_POINT}" 2>&1 |
    tee -a "${TAPE_DIR}/ltfs.log"
mountpoint -q "${MOUNT_POINT}"

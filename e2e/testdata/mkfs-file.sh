#!/usr/bin/env bash
set -euo pipefail

rm -rf -- "${DEVICE}"
mkdir -p -- "${DEVICE}"
mkltfs -f -e file -d "${DEVICE}" -s "${TAPE_BARCODE}" -n "${TAPE_NAME}" \
    -r 'size=512k' 2>&1 |
    tee -a "${TAPE_DIR}/ltfs.log"

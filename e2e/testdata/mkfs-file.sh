#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
rm -rf -- "${DEVICE}"
mkdir -p -- "${DEVICE}"
mkltfs -f -e file -d "${DEVICE}" -s "${TAPE_BARCODE}" -n "${TAPE_NAME}" \
    -r 'size=1M/name=*.fixture' 2>&1 |
    tee -a "${TAPE_DIR}/ltfs.log"

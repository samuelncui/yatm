#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
mkdir -p -- "${TAPE_DIR}"
exec > >(tee -a "${TAPE_DIR}/ltfs.log") 2>&1

script_dir=$(cd "$(dirname "$0")" && pwd)
sg_device=$("${script_dir}/get-device-physical.sh")

mkltfs -f -d "${sg_device}" -s "${TAPE_BARCODE}" -n "${TAPE_NAME}" \
    -r 'size=1M/name=*.txt'
sleep 3

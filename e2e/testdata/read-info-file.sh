#!/usr/bin/env bash
set -euo pipefail

barcode=${DEVICE##*/}
printf '{"barcode":"%s"}\n' "${barcode}" > "${OUT}"

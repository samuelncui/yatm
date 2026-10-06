#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
exec "${script_dir}/../ltfs-file-backend/umount"

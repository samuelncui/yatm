#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
"${script_dir}/umount-file.sh"

work_dir="${TAPE_DIR}"
for _ in 1 2 3 4; do
    work_dir="${work_dir%/*}"
done
unmount_marker="${work_dir}/.e2e-unmount-failed-once"
if [[ ! -e ${unmount_marker} ]]; then
    touch "${unmount_marker}"
    rmdir -- "${MOUNT_POINT}"
    echo "Injected failure after successful LTFS unmount" >&2
    exit 1
fi

index_marker="${work_dir}/.e2e-index-corrupted-once"
if [[ ! -e ${index_marker} ]]; then
    touch "${index_marker}"
    barcode=${TAPE_DIR##*/}
    index="${TAPE_DIR}/${barcode}.schema"
    for _ in $(seq 1 100); do
        if [[ -s ${index} ]]; then
            printf 'invalid final index\n' > "${index}"
            exit 0
        fi
        sleep 0.05
    done
    echo "LTFS final Index did not appear: ${index}" >&2
    exit 1
fi

exit 0

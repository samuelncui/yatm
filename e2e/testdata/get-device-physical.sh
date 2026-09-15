#!/usr/bin/env bash
set -euo pipefail

device=$(readlink -f "${DEVICE}")
if [[ ! ${device} =~ ^/dev/n?st([0-9]+)[alm]?$ ]]; then
    echo "unsupported Tape device: ${device}" >&2
    exit 1
fi

number=${BASH_REMATCH[1]}
for candidate in "/dev/nst${number}" "/dev/st${number}"; do
    sg_device=$(sg_map | awk -v device="${candidate}" '$2 == device { print $1; exit }')
    if [[ -n ${sg_device} ]]; then
        echo "${sg_device}"
        exit 0
    fi
done

echo "Tape device is not mapped: ${device}" >&2
exit 1

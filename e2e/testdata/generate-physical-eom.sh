#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
    echo "usage: $0 OUTPUT_DIRECTORY CHUNK_COUNT CHUNK_GIB" >&2
    exit 2
fi

output_directory=$1
chunk_count=$2
chunk_gib=$3
manifest="${output_directory}.sha256sum"
key=7df4fd97a80957d40e8a0be64ffb54f503f857407132bee657fee95fe2ee88c3

if [[ -e ${output_directory} || -e ${manifest} ]]; then
    echo "output already exists: ${output_directory}" >&2
    exit 1
fi
mkdir -p -- "${output_directory}"
: > "${manifest}"
for ((index = 0; index < chunk_count; index++)); do
    name=$(printf 'chunk-%03d.bin' "${index}")
    iv=$(printf '%032x' "${index}")
    echo "generating ${name} (${chunk_gib} GiB)" >&2
    dd if=/dev/zero bs=16M count=$((chunk_gib * 64)) status=none |
        openssl enc -aes-256-ctr -K "${key}" -iv "${iv}" -nosalt |
        tee "${output_directory}/${name}" |
        sha256sum |
        awk -v name="${name}" '{ print $1 "  " name }' >> "${manifest}"
done

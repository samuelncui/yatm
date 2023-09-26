#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

export TARGET_FILE="tapemanager-linux-amd64-${RELEASE_VERSION}.tar.gz"

rm -rf output;
mkdir -p output;

cp -r scripts ./output/;
cp ./cmd/tape-httpd/tape-writer.service ./output/
cp ./cmd/tape-httpd/config.example.yaml ./output/
cp ./LICENSE ./output/
cp ./README.md ./output/

# docker run --rm -v $(pwd):/app golang:1.21 sh -c "cd /app && bash "
# docker run --rm -v $(pwd):/app node:20-slim sh -c "cd /app && bash build_frontend.sh"
./build_backend.sh
./build_frontend.sh

tar -czvf "${TARGET_FILE}" -C ./output .

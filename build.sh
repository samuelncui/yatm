#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

export TARGET_FILE="yatm-${TARGET_NAME}-${RELEASE_VERSION}.tar.gz"

rm -rf output;
mkdir -p output;
mkdir -p output/captured_indices;

cp -r scripts ./output/;
cp ./cmd/httpd/yatm-httpd.service ./output/
cp ./cmd/httpd/config.example.yaml ./output/
cp ./LICENSE ./output/
cp ./README.md ./output/
echo "${RELEASE_VERSION}" > ./output/VERSION

# docker run --rm -v $(pwd):/app golang:1.21 sh -c "cd /app && bash build_backend.sh"
# docker run --rm -v $(pwd):/app node:20-slim sh -c "cd /app && bash build_frontend.sh"
./build_backend.sh
./build_frontend.sh

tar -czvf "${TARGET_FILE}" -C ./output .

#!/usr/bin/env bash
set -e;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

rm -rf output;
mkdir -p output;

cp -r scripts ./output/;
cp ./cmd/tape-httpd/tape-writer.service ./output/
cp ./cmd/tape-httpd/config.example.yaml ./output/

# docker run --rm -v $(pwd):/app golang:1.21 sh -c "cd /app && bash "
# docker run --rm -v $(pwd):/app node:20-slim sh -c "cd /app && bash build_frontend.sh"
./build_backend.sh
./build_frontend.sh

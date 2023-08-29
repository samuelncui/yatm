#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

rm -rf output;
mkdir -p output;
go build -mod=vendor -o ./output/httpd ./cmd/tape-httpd;
go build -mod=vendor -o ./output/lto-info ./cmd/lto-info;

cp -r scripts ./output/;
cp -r ./frontend/dist ./output/frontend;
cp ./cmd/tape-httpd/tape-writer.service ./output/
cp ./cmd/tape-httpd/config.example.yaml ./output/

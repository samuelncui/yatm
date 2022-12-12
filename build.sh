#!/usr/bin/env bash
set -e;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

rm -rf output;
mkdir -p output;
go build -o ./output/httpd ./cmd/tape-httpd;
go build -o ./output/loadtape ./cmd/tape-loadtape;
go build -o ./output/import ./cmd/tape-import;

cp -r scripts ./output/;
cp -r ./frontend/dist ./output/frontend;
cp ./cmd/tape-httpd/tape-writer.service ./output/
cp ./cmd/tape-httpd/config.example.yaml ./output/

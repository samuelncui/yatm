#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

go build -o ./output/httpd ./cmd/tape-httpd;
go build -o ./output/lto-info ./cmd/lto-info;

#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

go build -o ./output/yatm-httpd ./cmd/httpd;
go build -o ./output/yatm-export-library ./cmd/export-library;
go build -o ./output/yatm-lto-info ./cmd/lto-info;

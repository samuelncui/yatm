#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

CGO_ENABLED=1 \
    GOOS=linux \
    GOARCH=amd64 \
    CC="zig cc -target x86_64-linux-musl" \
    CXX="zig c++ -target x86_64-linux-musl" \
    ./build_backend.sh

#!/usr/bin/env bash
set -ex

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

SRC_DIR=${CURDIR};
GO_DST_DIR=${CURDIR};
TS_DST_DIR=${CURDIR}/../frontend/src/apis;

protoc --go_out=$GO_DST_DIR --go_opt=paths=source_relative \
    --go-grpc_out=$GO_DST_DIR --go-grpc_opt=paths=source_relative \
    -I=$SRC_DIR `ls *.proto`;

    # --js_out=import_style=es6,binary:$TS_DST_DIR \
    # --grpc-web_out=import_style=typescript,mode=grpcwebtext:$TS_DST_DIR \

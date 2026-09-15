#!/usr/bin/env bash
set -ex

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

SRC_DIR=${CURDIR};
GO_DST_DIR=${CURDIR};
TS_DST_DIR=${CURDIR}/../frontend/src/apis;

# Retired protobuf sources must not leave callable generated services behind.
for artifact in "$CURDIR"/*.pb.go; do
    [[ "$artifact" == *_grpc.pb.go ]] && continue
    base=$(basename "$artifact" .pb.go)
    [[ -f "$CURDIR/$base.proto" ]] && continue
    for output in "$artifact" "$CURDIR/${base}_grpc.pb.go" "$CURDIR/../frontend/src/entity/${base}.ts" "$CURDIR/../frontend/src/entity/${base}.client.ts"; do
        if [[ -f "$output" ]] && rg -q '@generated|Code generated' "$output"; then
            rm -f -- "$output"
        fi
    done
done

protoc --go_out=$GO_DST_DIR --go_opt=paths=source_relative \
    --go-grpc_out=$GO_DST_DIR --go-grpc_opt=paths=source_relative \
    -I=$SRC_DIR `ls *.proto`;

# Remove only generated RPC artifacts whose source no longer declares a service.
for source in ./*.proto; do
    if rg -q '^[[:space:]]*service[[:space:]]' "$source"; then
        continue
    fi
    base=$(basename "$source" .proto)
    for artifact in "$CURDIR/${base}_grpc.pb.go" "$CURDIR/../frontend/src/entity/${base}.client.ts"; do
        if [[ -f "$artifact" ]] && rg -q '@generated|Code generated' "$artifact"; then
            rm -f -- "$artifact"
        fi
    done
done

    # --js_out=import_style=es6,binary:$TS_DST_DIR \
    # --grpc-web_out=import_style=typescript,mode=grpcwebtext:$TS_DST_DIR \

go generate ./...

# The frozen legacy wire adapter is generated independently of the current API.
LEGACY_DIR="$CURDIR/../migrate/legacy/pb"
protoc --go_out="$LEGACY_DIR" --go_opt=paths=source_relative -I="$LEGACY_DIR" "$LEGACY_DIR/legacy.proto"
if [[ -f "$LEGACY_DIR/v1.pb.go" ]] && rg -q 'Code generated' "$LEGACY_DIR/v1.pb.go"; then
    rm -f -- "$LEGACY_DIR/v1.pb.go"
fi

cd ../frontend;
pnpm run gen-proto;

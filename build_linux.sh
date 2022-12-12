#!/usr/bin/env bash
set -e;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

docker run --rm -v $(pwd):/app golang:1.19 sh -c "cd /app && bash build.sh"

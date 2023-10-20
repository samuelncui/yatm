#!/usr/bin/env bash
set -ex;

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR}/frontend;

export PNPM_HOME="/pnpm";
export PATH="$PNPM_HOME:$PATH";
corepack enable;
pnpm install;
pnpm run build;

cp -r ./dist ../output/frontend;

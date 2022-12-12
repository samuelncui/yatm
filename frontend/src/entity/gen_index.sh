#!/usr/bin/env bash
set -e

CURDIR=$(cd $(dirname $0); pwd);
cd ${CURDIR};

echo '' > index.ts;

FILES=`ls *.ts | grep -v index.ts | sed -e 's/\.ts$//'`;
for file in ${FILES}; do
    echo "export * from \"./${file}\";" >> index.ts;
done

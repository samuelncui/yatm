#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
: "${RELEASE_VERSION:?Set RELEASE_VERSION to the candidate tag}"
: "${TARGET_NAME:?Set TARGET_NAME to the release platform name}"
export RELEASE_COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
export RELEASE_VERSION
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release version' >&2; exit 1; }
[[ "$RELEASE_COMMIT" =~ ^[a-f0-9]{40}$ ]] || { echo 'Invalid release commit' >&2; exit 1; }
[[ "$TARGET_NAME" =~ ^[a-z0-9-]+$ ]] || { echo 'Invalid target name' >&2; exit 1; }

# Build in a fresh directory; never reuse stale binaries or delete a user path.
PACKAGE_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/yatm-package.XXXXXX")"
trap 'rm -rf -- "$PACKAGE_DIRECTORY"' EXIT
export OUTPUT_DIRECTORY="$PACKAGE_DIRECTORY"
mkdir -p "$OUTPUT_DIRECTORY/templates/scripts" "$OUTPUT_DIRECTORY/skills/yatm"

# Only distributable runtime assets enter the package. User configuration is a template.
for script in encrypt get_device mkfs mount mount.openltfs readinfo umount; do
  cp "scripts/$script" "$OUTPUT_DIRECTORY/templates/scripts/"
done
cp cmd/httpd/yatm-httpd.service config.example.yaml "$OUTPUT_DIRECTORY/templates/"
cp LICENSE README.md CONTEXT.md "$OUTPUT_DIRECTORY/"
cp -R docs "$OUTPUT_DIRECTORY/"
cp .agents/skills/yatm/SKILL.md "$OUTPUT_DIRECTORY/skills/yatm/"
node build_documents.mjs "$OUTPUT_DIRECTORY" "$RELEASE_VERSION"
printf '%s\n' "$RELEASE_VERSION" > "$OUTPUT_DIRECTORY/VERSION"
printf '%s\n' "$RELEASE_COMMIT" > "$OUTPUT_DIRECTORY/COMMIT"

./build_backend.sh
./build_frontend.sh
node build_licenses.mjs "$OUTPUT_DIRECTORY/licenses"

# Each archive has a portable companion checksum; CI aggregates only complete target sets.
TARGET_FILE="yatm-${TARGET_NAME}-${RELEASE_VERSION}.tar.gz"
COPYFILE_DISABLE=1 tar --no-xattrs --no-acls -czf "$TARGET_FILE" -C "$OUTPUT_DIRECTORY" .
node -e 'const fs=require("node:fs"),crypto=require("node:crypto");const p=process.argv[1];fs.writeFileSync(p+".sha256",crypto.createHash("sha256").update(fs.readFileSync(p)).digest("hex")+"  "+p+"\n")' "$TARGET_FILE"
node check_release.mjs "$TARGET_FILE"

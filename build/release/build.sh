#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
RELEASE_DIRECTORY="${RELEASE_DIRECTORY:-$REPOSITORY/output/releases}"
case "$RELEASE_DIRECTORY" in /*) ;; *) RELEASE_DIRECTORY="$PWD/$RELEASE_DIRECTORY" ;; esac
export RELEASE_DIRECTORY
if [[ -n "${FRONTEND_ARTIFACT_DIRECTORY:-}" && "$FRONTEND_ARTIFACT_DIRECTORY" != /* ]]; then
  FRONTEND_ARTIFACT_DIRECTORY="$PWD/$FRONTEND_ARTIFACT_DIRECTORY"
fi
cd "$REPOSITORY"
: "${RELEASE_VERSION:?Set RELEASE_VERSION to the candidate tag}"
: "${TARGET_NAME:?Set TARGET_NAME to the release platform name}"
export RELEASE_VERSION
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release version' >&2; exit 1; }
[[ "$TARGET_NAME" =~ ^[a-z0-9-]+$ ]] || { echo 'Invalid target name' >&2; exit 1; }

# Build in a fresh directory; never reuse stale binaries or delete a user path.
PACKAGE_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/yatm-package.XXXXXX")"
trap 'rm -rf -- "$PACKAGE_DIRECTORY"' EXIT
export OUTPUT_DIRECTORY="$PACKAGE_DIRECTORY/package"
RELEASE_COMMIT="$(node "$REPOSITORY/build/release/committed-inputs.mjs" checkout "$REPOSITORY" "$PACKAGE_DIRECTORY/source" "${RELEASE_COMMIT:-}")"
export RELEASE_COMMIT
REPOSITORY="$PACKAGE_DIRECTORY/source"
cd "$REPOSITORY"
if [[ "${GOOS:-$(go env GOOS)}" == linux ]]; then
  # Keep the distributed license graph aligned with the forced C SQLite build.
  export CGO_ENABLED=1
fi
mkdir -p "$RELEASE_DIRECTORY"
mkdir -p "$OUTPUT_DIRECTORY/templates/scripts" "$OUTPUT_DIRECTORY/skills/yatm" "$OUTPUT_DIRECTORY/docs/operations"

# Only distributable runtime assets enter the package. User configuration is a template.
for script in encrypt get_device mkfs mount mount.openltfs readinfo umount; do
  cp "scripts/$script" "$OUTPUT_DIRECTORY/templates/scripts/"
done
mkdir -p "$OUTPUT_DIRECTORY/templates/testing/ltfs-file-backend"
for script in encrypt get_device mkfs mount readinfo umount README.md; do
  cp "e2e/ltfs-file-backend/$script" "$OUTPUT_DIRECTORY/templates/testing/ltfs-file-backend/"
done
cp cmd/httpd/yatm-httpd.service config.example.yaml "$OUTPUT_DIRECTORY/templates/"
cp LICENSE README.md CONTEXT.md install-release.sh "$OUTPUT_DIRECTORY/"
cp docs/README.md "$OUTPUT_DIRECTORY/docs/"
for guide in install migration library locations; do
  cp "docs/operations/$guide.md" "$OUTPUT_DIRECTORY/docs/operations/"
done
cp .agents/skills/yatm/SKILL.md "$OUTPUT_DIRECTORY/skills/yatm/"
node build/release/build-documents.mjs "$OUTPUT_DIRECTORY" "$RELEASE_VERSION"
printf '%s\n' "$RELEASE_VERSION" > "$OUTPUT_DIRECTORY/VERSION"
printf '%s\n' "$RELEASE_COMMIT" > "$OUTPUT_DIRECTORY/COMMIT"

if [[ -z "${FRONTEND_ARTIFACT_DIRECTORY:-}" ]]; then
  FRONTEND_ARTIFACT_DIRECTORY="$PACKAGE_DIRECTORY/shared-frontend"
  bash build/release/build-frontend.sh "$FRONTEND_ARTIFACT_DIRECTORY"
fi
node build/release/frontend-artifact.mjs install "$FRONTEND_ARTIFACT_DIRECTORY" "$OUTPUT_DIRECTORY"
./build/backend/build.sh
node build/release/build-licenses.mjs "$OUTPUT_DIRECTORY/licenses" backend

# Each archive has a portable companion checksum; CI aggregates only complete target sets.
TARGET_FILE="$RELEASE_DIRECTORY/yatm-${TARGET_NAME}-${RELEASE_VERSION}.tar.gz"
COPYFILE_DISABLE=1 tar --no-xattrs --no-acls -czf "$TARGET_FILE" -C "$OUTPUT_DIRECTORY" .
node -e 'const fs=require("node:fs"),crypto=require("node:crypto");const p=process.argv[1];fs.writeFileSync(p+".sha256",crypto.createHash("sha256").update(fs.readFileSync(p)).digest("hex")+"  "+require("node:path").basename(p)+"\n")' "$TARGET_FILE"
node build/release/check-release.mjs "$TARGET_FILE"

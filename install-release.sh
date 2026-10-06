#!/usr/bin/env bash
set -Eeuo pipefail

INSTALL_DIRECTORY=/opt/yatm
SERVICE_NAME=yatm-httpd.service
SKILLS_VERSION=1.5.0
STAGE=preflight
BACKUP_DIRECTORY=
BACKUP_COMPLETE=0
INSTALLATION_CHANGED=0
SERVICE_STOPPED=0
CONFIG_CHANGED=false
CONFIG_PLAN=
WORK_DIRECTORY=
REPORT_FILE=
ATTEMPT_DIRECTORY=
REPORT_DIRECTORY=
SERVICE_ACTIVE=0
SERVICE_PID=
RELEASE_VERSION=
LOCAL_ARCHIVE=
CHECKSUM_FILE=
FRESH_CONFIG=
PREVIEW_CHOICE=ask
PREVIEW_ARCHIVE=
PREVIEW_CHECKSUM=
INSTALL_PREVIEW=0
CHECK_ONLY=0
SHOW_HELP=0
LEGACY=0
REQUIRED_TOOLS=(curl jq tar sha256sum systemctl cp du df readlink mktemp find flock tee awk grep sed mv rm mkdir rmdir chmod date sleep id uname cat)
MANAGED_ITEMS=(yatm-httpd yatm-cli yatm-export-library yatm-lto-info yatm-migrate install-release.sh frontend README.md CONTEXT.md docs VERSION COMMIT LICENSE licenses skills templates)
OBSOLETE_MANAGED_ITEMS=(config.example.yaml)

fail() { echo "error: $*" >&2; return 1; }
confirm_action() {
  local reply
  read -r -p "$1 [y/N] " reply || return 1
  [[ "$reply" == y || "$reply" == Y ]]
}

usage() {
  echo 'Usage: install-release.sh [--version v1.0.0-alpha.1] [--check]'
  echo '       [--archive FILE --checksum FILE] [--install-dir DIR] [--service NAME]'
  echo '       [--config FILE]'
  echo '       [--with-preview | --without-preview] [--preview-archive FILE --preview-checksum FILE]'
  echo "Linux amd64/systemd only. Required tools: ${REQUIRED_TOOLS[*]}."
  echo 'Stable is the default. Alpha and local candidates require an explicit version.'
  echo '--check performs read-only checks; it does not stop or quiesce YATM.'
  echo '--config supplies the configuration for a fresh installation only.'
}

parse_options() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version|--archive|--checksum|--install-dir|--service|--config|--preview-archive|--preview-checksum)
        [[ $# -ge 2 && "$2" != --* && -n "$2" ]] || { fail "Missing value for $1."; return 1; } ;;
    esac
    case "$1" in
      --version) RELEASE_VERSION="v${2#v}"; shift 2 ;;
      --archive) LOCAL_ARCHIVE="${2:?Missing archive}"; shift 2 ;;
      --checksum) CHECKSUM_FILE="${2:?Missing checksum}"; shift 2 ;;
      --install-dir) INSTALL_DIRECTORY="${2:?Missing installation directory}"; shift 2 ;;
      --service) SERVICE_NAME="${2:?Missing service name}"; shift 2 ;;
      --config) FRESH_CONFIG="${2:?Missing configuration}"; shift 2 ;;
      --with-preview) PREVIEW_CHOICE=yes; shift ;;
      --without-preview) PREVIEW_CHOICE=no; shift ;;
      --preview-archive) PREVIEW_ARCHIVE="$2"; PREVIEW_CHOICE=yes; shift 2 ;;
      --preview-checksum) PREVIEW_CHECKSUM="$2"; shift 2 ;;
      --check) CHECK_ONLY=1; shift ;;
      -h|--help) SHOW_HELP=1; shift ;;
      *) fail "Unknown option: $1"; return 1 ;;
    esac
  done
}

prepare_preview() {
  # Optional dependencies are reviewed and staged before service admission or a complete backup.
  [[ "$LEGACY" == 0 ]] || return 0
  if [[ "$PREVIEW_CHOICE" == ask ]]; then
    if [[ "$CHECK_ONLY" == 0 && -t 0 ]] && confirm_action 'Install the optional Image/Video Preview helper? Generation stays disabled until enabled in Settings.'; then
      PREVIEW_CHOICE=yes
    else
      echo 'Optional Preview helper not selected. Use --with-preview to install it later.'
      return 0
    fi
  fi
  [[ "$PREVIEW_CHOICE" == yes ]] || return 0
  local name="yatm-preview-linux-amd64-$RELEASE_VERSION.tar.gz"
  local archive="$TMP_DIRECTORY/$name" checksum="$TMP_DIRECTORY/preview-checksum"
  local url="https://github.com/samuelncui/yatm/releases/download/$RELEASE_VERSION/$name"
  if [[ -n "$PREVIEW_ARCHIVE" ]]; then
    [[ -n "$PREVIEW_CHECKSUM" ]] || { fail '--preview-archive requires --preview-checksum.'; return 1; }
    cp "$PREVIEW_ARCHIVE" "$archive"
  else
    fetch "$url" -o "$archive"
  fi
  if [[ -n "$PREVIEW_CHECKSUM" ]]; then cp "$PREVIEW_CHECKSUM" "$checksum"; else fetch "$url.sha256" -o "$checksum"; fi
  local -a lines
  local expected actual ignored member
  mapfile -t lines < "$checksum"
  [[ "${#lines[@]}" == 1 ]] || { fail 'Expected one Preview checksum.'; return 1; }
  read -r expected ignored <<< "${lines[0]}"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || { fail 'Invalid Preview checksum.'; return 1; }
  actual="$(sha256sum "$archive")"; actual="${actual%% *}"
  [[ "${expected,,}" == "$actual" ]] || { fail 'Preview checksum mismatch.'; return 1; }

  # This package can only replace its three owned resources, never configuration or main executables.
  tar -tzf "$archive" > "$TMP_DIRECTORY/preview-members"
  while IFS= read -r member; do
    member="${member#./}"
    [[ "$member" != /* && "$member" != '..' && "$member" != ../* && "$member" != */../* && "$member" != */.. ]] || {
      fail 'Unsafe Preview archive member.'; return 1;
    }
    case "$member" in ''|yatm-preview|preview-lib|preview-lib/*|preview-support|preview-support/*) ;;
      *) fail "Unexpected Preview archive member: $member"; return 1 ;;
    esac
  done < "$TMP_DIRECTORY/preview-members"
  tar -tvzf "$archive" > "$TMP_DIRECTORY/preview-member-types"
  if LC_ALL=C grep -qvE '^[-d]' "$TMP_DIRECTORY/preview-member-types"; then
    fail 'Preview archives may only contain ordinary files and directories.'; return 1
  fi
  tar -xzf "$archive" --no-same-owner --preserve-permissions -C "$RELEASE_DIRECTORY"
  [[ -x "$RELEASE_DIRECTORY/yatm-preview" && -d "$RELEASE_DIRECTORY/preview-lib" && -f "$RELEASE_DIRECTORY/preview-support/VERSION" ]] || {
    fail 'Incomplete Preview package.'; return 1;
  }
  [[ "$(< "$RELEASE_DIRECTORY/preview-support/VERSION")" == "$RELEASE_VERSION" ]] || { fail 'Preview package version mismatch.'; return 1; }
  [[ -f "$RELEASE_DIRECTORY/preview-support/COMMIT" && "$(< "$RELEASE_DIRECTORY/preview-support/COMMIT")" == "$(< "$RELEASE_DIRECTORY/COMMIT")" ]] || {
    fail 'Preview package commit mismatch.'; return 1;
  }
  if ! "$RELEASE_DIRECTORY/yatm-preview" --capabilities | jq -e --arg version "$RELEASE_VERSION" \
    '.protocol == 1 and .type == "capabilities" and .capabilities.version == $version' >/dev/null; then
    fail 'Preview helper cannot run or has an incompatible protocol/version.'; return 1
  fi
  local item
  for item in yatm-preview preview-lib preview-support; do
    if [[ -e "$INSTALL_DIRECTORY/$item" || -L "$INSTALL_DIRECTORY/$item" ]]; then
      [[ -f "$INSTALL_DIRECTORY/preview-support/VERSION" && -f "$INSTALL_DIRECTORY/preview-support/COMMIT" && ! -L "$INSTALL_DIRECTORY/$item" ]] || {
        fail "Unrecognized local Preview resource: $item. Preserve it before installing the managed helper."; return 1;
      }
    fi
  done
  MANAGED_ITEMS+=(yatm-preview preview-lib preview-support)
  INSTALL_PREVIEW=1
  echo 'Optional Preview helper verified; existing generation preferences will be retained.'
}

version_key() {
  local version="$1" rank=3 sequence=0
  [[ "$version" =~ ^v([0-9]{1,5})\.([0-9]{1,5})\.([0-9]{1,5})(-(alpha|beta|rc)\.([0-9]{1,5}))?$ ]] || return 1
  local major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}" pre="${BASH_REMATCH[5]:-}"
  sequence="${BASH_REMATCH[6]:-0}"
  case "$pre" in alpha) rank=0 ;; beta) rank=1 ;; rc) rank=2 ;; esac
  printf '%05d.%05d.%05d.%d.%05d' "$((10#$major))" "$((10#$minor))" "$((10#$patch))" "$rank" "$((10#$sequence))"
}

fetch() {
  command curl -q --fail --location --silent --show-error --connect-timeout 15 --max-time 180 \
    --retry 2 --retry-delay 2 --proto '=https' --proto-redir '=https' "$@"
}

identify_platform() {
  [[ "$(uname -s)" == Linux && "$(uname -m)" =~ ^(x86_64|amd64)$ ]] || {
    fail 'Automatic installation supports Linux amd64/systemd. Use the manual guide on other platforms.'; return 1;
  }
  local dependency
  for dependency in "${REQUIRED_TOOLS[@]}"; do
    command -v "$dependency" >/dev/null || { fail "Install the required tool '$dependency' first."; return 1; }
  done
  [[ "$(tar --version)" == *'GNU tar'* ]] || { fail 'GNU tar is required for complete backup verification.'; return 1; }
  [[ "$INSTALL_DIRECTORY" =~ ^/[a-zA-Z0-9_./-]+$ && "$INSTALL_DIRECTORY" != */../* && "$INSTALL_DIRECTORY" != */.. ]] || {
    fail 'Installation directory must be an absolute path without whitespace or parent traversal.'; return 1;
  }
  INSTALL_DIRECTORY="$(readlink -m "$INSTALL_DIRECTORY")"
  case "$INSTALL_DIRECTORY" in /|/opt|/usr|/usr/local|/var|/tmp|/home|/root) fail 'Choose a dedicated YATM installation directory.'; return 1 ;; esac
  [[ "$SERVICE_NAME" =~ ^[a-zA-Z0-9_-]+\.service$ ]] || { fail 'Invalid systemd service name.'; return 1; }
}

begin_attempt() {
  [[ "$CHECK_ONLY" == 0 ]] || return 0
  local upgrades="$INSTALL_DIRECTORY/.backup"
  umask 077
  mkdir -p -m 755 "$INSTALL_DIRECTORY"
  if [[ -e "$upgrades" || -L "$upgrades" ]]; then
    [[ -d "$upgrades" && ! -L "$upgrades" ]] || {
      fail 'Reserved .backup path is not a directory.'; return 1;
    }
  else
    mkdir -m 700 "$upgrades"
  fi
  chmod 700 "$upgrades"
  [[ ! -L "$upgrades/lock" && ( ! -e "$upgrades/lock" || -f "$upgrades/lock" ) ]] || {
    fail 'Backup lock is not a regular file.'; return 1;
  }
  exec 9> "$upgrades/lock"
  flock -n 9 || { fail 'Another installer is using this installation.'; return 1; }
  # Honor a running older installer before replacing its retained-artifact convention.
  local old_lock="$INSTALL_DIRECTORY/.yatm-upgrades/lock"
  if [[ -f "$old_lock" && ! -L "$old_lock" && ! -L "$INSTALL_DIRECTORY/.yatm-upgrades" ]]; then
    exec 8<> "$old_lock"
    flock -n 8 || { fail 'An older installer is using this installation.'; return 1; }
  fi
  reject_pending_migration
  ATTEMPT_DIRECTORY="$(mktemp -d "$upgrades/$(date +%Y%m%d%H%M%S).XXXXXX")"
  REPORT_DIRECTORY="$ATTEMPT_DIRECTORY"
  WORK_DIRECTORY="$ATTEMPT_DIRECTORY/.work"
  mkdir -m 700 "$WORK_DIRECTORY"
  TMP_DIRECTORY="$WORK_DIRECTORY"
  RELEASE_DIRECTORY="$WORK_DIRECTORY/release"
  REPORT_FILE="$REPORT_DIRECTORY/upgrade.log"
  exec > >(tee -a "$REPORT_FILE") 2>&1
  echo "Installation report: $REPORT_FILE"
}

reject_ambiguous_fresh_root() {
  [[ ! -e "$INSTALL_DIRECTORY" && ! -L "$INSTALL_DIRECTORY" ]] && return 0
  [[ -d "$INSTALL_DIRECTORY" && ! -L "$INSTALL_DIRECTORY" ]] || {
    fail 'Installation path exists but is not a directory.'; return 1;
  }
  [[ -f "$INSTALL_DIRECTORY/VERSION" ]] && return 0
  local backups="$INSTALL_DIRECTORY/.backup"
  if [[ -e "$backups" || -L "$backups" ]]; then
    [[ -d "$backups" && ! -L "$backups" ]] || {
      fail 'Reserved .backup path is not a directory.'; return 1;
    }
  fi
  # A cancelled fresh installation may retain reports without having installed a VERSION.
  if [[ -n "$(find "$INSTALL_DIRECTORY" -mindepth 1 -maxdepth 1 ! -name .backup -print -quit)" ]]; then
    fail 'Refusing to use a non-empty installation directory without a recognized VERSION. Inspect or move its contents first.'; return 1
  fi
}

resolve_version() {
  if [[ -z "$RELEASE_VERSION" ]]; then
    [[ -z "$LOCAL_ARCHIVE" ]] || { fail '--archive requires --version.'; return 1; }
    fetch https://api.github.com/repos/samuelncui/yatm/releases/latest > "$TMP_DIRECTORY/latest.json"
    RELEASE_VERSION="$(jq -er '.tag_name | select(type == "string")' "$TMP_DIRECTORY/latest.json")"
  fi
  TARGET_KEY="$(version_key "$RELEASE_VERSION")" || { fail "Unsupported version: $RELEASE_VERSION"; return 1; }
  if [[ "$RELEASE_VERSION" =~ ^v0\.1\.([0-9]+)$ ]] && (( 10#${BASH_REMATCH[1]} <= 21 )); then LEGACY=1; fi
  if [[ -f "$INSTALL_DIRECTORY/VERSION" ]]; then
    CURRENT_VERSION="$(< "$INSTALL_DIRECTORY/VERSION")"
    CURRENT_VERSION="v${CURRENT_VERSION#v}"
    local current_key
    current_key="$(version_key "$CURRENT_VERSION")" || { fail 'Installed VERSION is unrecognized; inspect the installation manually.'; return 1; }
    if [[ "$TARGET_KEY" < "$current_key" ]]; then
      fail "Refusing downgrade from $CURRENT_VERSION to $RELEASE_VERSION. Select an explicit compatible version; rollback requires its complete backup."; return 1
    fi
  else
    CURRENT_VERSION=none
    [[ ! -f "$INSTALL_DIRECTORY/config.yaml" ]] || { fail 'Existing installation has no VERSION. Use the manual upgrade guide.'; return 1; }
  fi
}

download_release() {
  local name="yatm-linux-amd64-$RELEASE_VERSION.tar.gz" url
  ARCHIVE="$TMP_DIRECTORY/$name"
  url="https://github.com/samuelncui/yatm/releases/download/$RELEASE_VERSION/$name"
  if [[ -n "$LOCAL_ARCHIVE" ]]; then
    [[ -n "$CHECKSUM_FILE" ]] || { fail 'A local candidate requires --checksum.'; return 1; }
    cp "$LOCAL_ARCHIVE" "$ARCHIVE"
  else
    fetch "$url" -o "$ARCHIVE"
  fi
  if [[ -n "$CHECKSUM_FILE" ]]; then
    cp "$CHECKSUM_FILE" "$TMP_DIRECTORY/checksum"
  elif [[ "$LEGACY" == 0 ]]; then
    fetch "$url.sha256" -o "$TMP_DIRECTORY/checksum"
  else
    echo "Manual check: legacy release $RELEASE_VERSION has no publisher checksum. Source: $url"
  fi
  if [[ -f "$TMP_DIRECTORY/checksum" ]]; then
    local expected actual extra
    local -a checksum_lines
    mapfile -t checksum_lines < "$TMP_DIRECTORY/checksum"
    [[ "${#checksum_lines[@]}" == 1 ]] || { fail 'Expected one SHA-256 checksum.'; return 1; }
    read -r expected extra <<< "${checksum_lines[0]}"
    [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || { fail 'Expected one SHA-256 checksum.'; return 1; }
    actual="$(sha256sum "$ARCHIVE")"; actual="${actual%% *}"
    [[ "${expected,,}" == "$actual" ]] || { fail 'Release checksum mismatch.'; return 1; }
  fi

  # Inspect names and entry types before extracting or executing candidate programs.
  tar -tzf "$ARCHIVE" > "$TMP_DIRECTORY/members"
  local member
  while IFS= read -r member; do
    [[ "$member" != /* && "$member" != '..' && "$member" != ../* && "$member" != */../* && "$member" != */.. ]] || {
      fail 'Unsafe archive member.'; return 1;
    }
  done < "$TMP_DIRECTORY/members"
  tar -tvzf "$ARCHIVE" > "$TMP_DIRECTORY/member-types"
  if LC_ALL=C grep -qvE '^[-d]' "$TMP_DIRECTORY/member-types"; then
    fail 'Release archives may only contain ordinary files and directories.'; return 1
  fi
  mkdir "$RELEASE_DIRECTORY"
  tar -xzf "$ARCHIVE" --no-same-owner --preserve-permissions -C "$RELEASE_DIRECTORY"
  local archive_version
  [[ -f "$RELEASE_DIRECTORY/VERSION" ]] || { fail 'Archive VERSION is missing.'; return 1; }
  archive_version="$(< "$RELEASE_DIRECTORY/VERSION")"
  [[ "v${archive_version#v}" == "$RELEASE_VERSION" ]] || { fail 'Archive VERSION does not match the requested release.'; return 1; }
  local program
  for program in yatm-httpd yatm-export-library yatm-lto-info; do
    [[ -x "$RELEASE_DIRECTORY/$program" ]] || { fail "Missing executable $program."; return 1; }
  done
  [[ -f "$RELEASE_DIRECTORY/frontend/index.html" ]] || { fail 'Missing frontend.'; return 1; }
  if [[ "$LEGACY" == 1 ]]; then return; fi
  local item
  for item in "${MANAGED_ITEMS[@]}"; do
    [[ -e "$RELEASE_DIRECTORY/$item" ]] || { fail "Candidate is missing managed resource $item."; return 1; }
  done
  for program in yatm-httpd yatm-cli yatm-migrate yatm-export-library yatm-lto-info; do
    [[ -x "$RELEASE_DIRECTORY/$program" ]] || { fail "Missing executable $program."; return 1; }
    "$RELEASE_DIRECTORY/$program" --version | jq -e --arg program "$program" --arg version "$RELEASE_VERSION" \
      --arg commit "$(< "$RELEASE_DIRECTORY/COMMIT")" '.program == $program and .version == $version and .commit == $commit' >/dev/null
  done
  [[ -f "$RELEASE_DIRECTORY/templates/config.example.yaml" && -f "$RELEASE_DIRECTORY/templates/yatm-httpd.service" && -f "$RELEASE_DIRECTORY/skills/yatm/SKILL.md" ]] || {
    fail 'Candidate is missing installation templates or its bundled Skill.'; return 1;
  }
}

run_migrator() {
  (cd "$INSPECTION_DIRECTORY" && "$RELEASE_DIRECTORY/yatm-migrate" -config ./config.yaml "$@")
}

service_property() {
  systemctl show "$SERVICE_NAME" --property "$1" --value
}

service_main_pid() {
  local pid
  pid="$(service_property MainPID)" || return 1
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s\n' "$pid"
}

verify_service_ownership() {
  local load_state fragment working_directory
  load_state="$(service_property LoadState)" || return 1
  fragment="$(service_property FragmentPath)" || return 1
  working_directory="$(service_property WorkingDirectory)" || return 1
  if [[ "$CURRENT_VERSION" == none ]]; then
    [[ "$load_state" == not-found && -z "$fragment" && -z "$working_directory" ]] || {
      fail "Service $SERVICE_NAME already exists and is not owned by this fresh installation."; return 1;
    }
    return 0
  fi
  [[ "$load_state" != not-found ]] || { fail "Installed service unit $SERVICE_NAME was not found."; return 1; }
  local installation_root
  installation_root="$(readlink -m "$INSTALL_DIRECTORY")"
  [[ -n "$fragment" && "$(readlink -f "$fragment")" == "$installation_root/$SERVICE_NAME" ]] || {
    fail "The service unit is outside the installation backup: ${fragment:-missing}. Use manual upgrade."; return 1;
  }
  [[ -n "$working_directory" && "$(readlink -m "$working_directory")" == "$installation_root" ]] || {
    fail 'The configured service working directory differs from the installation. Use manual upgrade.'; return 1;
  }
}

prepare_fresh_config() {
  INSPECTION_DIRECTORY="$TMP_DIRECTORY/fresh"
  mkdir "$INSPECTION_DIRECTORY"
  local templates="$RELEASE_DIRECTORY/templates"
  [[ "$LEGACY" == 0 ]] || templates="$RELEASE_DIRECTORY"
  cp -a "$templates/scripts" "$INSPECTION_DIRECTORY/scripts"
  cp "${FRESH_CONFIG:-$templates/config.example.yaml}" "$INSPECTION_DIRECTORY/config.yaml"
  if [[ -z "$FRESH_CONFIG" && "$CHECK_ONLY" == 0 ]]; then
    [[ -t 0 ]] || { fail 'A noninteractive fresh installation requires --config.'; return 1; }
    "${EDITOR:-vi}" "$INSPECTION_DIRECTORY/config.yaml"
  fi
}

inspect_installation() {
  INSPECTION_DIRECTORY="$INSTALL_DIRECTORY"
  if [[ "$CURRENT_VERSION" == none ]]; then
    prepare_fresh_config
  else
    [[ -z "$FRESH_CONFIG" ]] || { fail '--config is only for a fresh installation; existing configuration is preserved.'; return 1; }
    if systemctl is-active --quiet "$SERVICE_NAME"; then SERVICE_ACTIVE=1; fi
  fi
  verify_service_ownership
  if [[ "$LEGACY" == 1 ]]; then
    [[ "$CURRENT_VERSION" == none || "$CURRENT_VERSION" == "$RELEASE_VERSION" ]] || {
      fail 'This legacy candidate has no read-only migration inspector. Use the legacy manual update guide or explicitly select current Alpha.'; return 1;
    }
    return
  fi
  local stopped=()
  [[ "$SERVICE_ACTIVE" == 1 ]] || stopped=(-service-stopped)
  if [[ "$SERVICE_ACTIVE" == 1 ]]; then
    SERVICE_PID="$(service_main_pid)" || { fail 'The active systemd service has no stable MainPID.'; return 1; }
    stopped+=(-service-pid "$SERVICE_PID")
  fi
  local inspection_root="$INSPECTION_DIRECTORY"
  if [[ "$CURRENT_VERSION" == none ]]; then
    stopped+=(-fresh-install)
    inspection_root="$INSTALL_DIRECTORY"
  fi
  local preflight_file="$TMP_DIRECTORY/preflight.json"
  run_migrator -phase preflight -json -install-root "$inspection_root" "${stopped[@]}" > "$preflight_file"
  if [[ "$SERVICE_ACTIVE" == 1 ]]; then
    [[ "$(service_main_pid)" == "$SERVICE_PID" ]] || { fail 'The systemd service changed during preflight.'; return 1; }
  fi
  jq . "$TMP_DIRECTORY/preflight.json"
  SCHEMA="$(jq -er .schema "$TMP_DIRECTORY/preflight.json")"
  SERVER_URL="$(jq -er .server_url "$TMP_DIRECTORY/preflight.json")"
  if [[ "$CURRENT_VERSION" != none && "$SCHEMA" != legacy && "$SCHEMA" != current ]]; then
    fail 'Existing installation has no recognized catalog. Inspect its configured database before upgrading.'; return 1
  fi
  if [[ "$CURRENT_VERSION" != none ]]; then
    local needed available candidate
    (cd "$INSTALL_DIRECTORY" && find . -mindepth 1 -maxdepth 1 ! -name .backup -print0) > "$TMP_DIRECTORY/backup.entries"
    needed="$(cd "$INSTALL_DIRECTORY" && du -skc --files0-from="$TMP_DIRECTORY/backup.entries" | awk 'END {print $1}')"
    candidate="$(du -sk "$RELEASE_DIRECTORY" | awk '{print $1}')"
    available="$(df -Pk "$INSTALL_DIRECTORY" | awk 'END {print $4}')"
    (( available > needed * 2 + candidate + 10240 )) || {
      fail 'Insufficient installation-filesystem space for the complete backup, migration and candidate.'; return 1;
    }
  fi
  echo 'Passed: configuration, backup scope and current service activity checks.'
  jq -r '.warnings[]? | "Manual check: " + .' "$TMP_DIRECTORY/preflight.json"
  echo 'Tape scripts were not executed; installation checks do not establish Tape readiness.'
}

show_migration_guide() {
  echo "Requested installation: $CURRENT_VERSION → $RELEASE_VERSION"
  [[ "$CURRENT_VERSION" != none ]] || return 0
  if [[ "$RELEASE_VERSION" == v1.* ]]; then
    echo 'Tape adapter check required before the first Tape Job: v1 needs the completed final Index at TAPE_DIR/<barcode>.schema.'
    echo 'Custom scripts are retained. Keep legacy captures through migration, then adapt mount output and wait for final Index output and device release in unmount.'
    echo 'Read the verified release guide: docs/operations/migration.md, Tape Script Adaptation.'
  fi
  local current_major="${CURRENT_VERSION#v}" target_major="${RELEASE_VERSION#v}"
  [[ "${current_major%%.*}" != "${target_major%%.*}" ]] || return 0
  [[ "$CURRENT_VERSION" =~ ^v0\.1\.[0-9]+$ && "$RELEASE_VERSION" == v1.* ]] || {
    fail 'This installer has no supported migration procedure for the requested major-version change.'; return 1;
  }
  local guide="$RELEASE_DIRECTORY/docs/operations/migration.md"
  [[ -s "$guide" && ! -L "$guide" ]] || { fail 'The verified release is missing its migration guide.'; return 1; }
  echo "Migration guide from the verified release: $guide"
  if [[ "$CHECK_ONLY" == 1 ]]; then
    echo 'Read this guide before the mutating run; that run displays it once before consent.'
    return 0
  fi
  command cat "$guide"
}

backup_installation() {
  STAGE=backup
  BACKUP_DIRECTORY="$ATTEMPT_DIRECTORY"
  BACKUP_COMPLETE=0
  echo "Installation backup destination: $BACKUP_DIRECTORY/yatm.tar.gz"
  # Archive the root itself to retain its metadata; only prior backups are excluded.
  local partial="$BACKUP_DIRECTORY/yatm.tar.gz.partial"
  local -a archive_options=(--format=pax --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime
    --sort=name --sparse --acls --xattrs --xattrs-include='*' --numeric-owner --exclude=./.backup)
  if ! tar "${archive_options[@]}" \
    -C "$INSTALL_DIRECTORY" -czf "$partial" .; then
    fail "Installation backup failed; incomplete archive remains at $partial."
    return 1
  fi
  if ! tar --acls --xattrs --xattrs-include='*' --numeric-owner -C "$INSTALL_DIRECTORY" -dzf "$partial"; then
    fail "Installation backup comparison failed; unverified archive remains at $partial."
    return 1
  fi
  # tar --compare does not verify extended attributes or detect newly added files.
  # A deterministic second stream checks the entire frozen tree, including ACL/xattr payloads.
  local saved observed
  saved="$(sha256sum "$partial")"
  observed="$(tar "${archive_options[@]}" -C "$INSTALL_DIRECTORY" -czf - . | sha256sum)" || {
    fail 'Could not verify the complete backup tree.'; return 1;
  }
  [[ "${saved%% *}" == "${observed%% *}" ]] || {
    fail 'Installation changed while verifying the backup; the archive remains unverified.'; return 1;
  }
  mv "$partial" "$BACKUP_DIRECTORY/yatm.tar.gz"
  (cd "$BACKUP_DIRECTORY" && sha256sum yatm.tar.gz > yatm.tar.gz.sha256)
  BACKUP_COMPLETE=1
  echo 'Passed: complete backup content and metadata comparison.'
}

reject_pending_migration() {
  local marker="$INSTALL_DIRECTORY/.yatm-upgrades/migration.pending.json"
  [[ ! -e "$marker" && ! -L "$marker" ]] || {
    fail 'An earlier upgrade is unfinished. Complete it using its original installer or recover its complete backup before this upgrade.'; return 1;
  }
}

plan_configuration() {
  [[ "$LEGACY" == 0 && "$CURRENT_VERSION" != none ]] || return 0
  CONFIG_PLAN="$TMP_DIRECTORY/config-plan.json"
  run_migrator -phase config-plan -plan-file "$CONFIG_PLAN" > "$TMP_DIRECTORY/config-summary.json"
  CONFIG_CHANGED="$(jq -er '.changed | tostring' "$TMP_DIRECTORY/config-summary.json")"
  [[ "$CONFIG_CHANGED" == true || "$CONFIG_CHANGED" == false ]] || return 1
  jq -r '.diff' "$TMP_DIRECTORY/config-summary.json" > "${REPORT_DIRECTORY:-$TMP_DIRECTORY}/config.diff"
  cat "${REPORT_DIRECTORY:-$TMP_DIRECTORY}/config.diff"
  jq -r '.settings[]? | "Settings: " + .' "$TMP_DIRECTORY/config-summary.json"
}

clean_work_directory() {
  [[ -n "$WORK_DIRECTORY" && "$WORK_DIRECTORY" == "$ATTEMPT_DIRECTORY/.work" ]] || return 0
  rm -rf -- "$WORK_DIRECTORY"
}

clean_old_upgrades() {
  local old="$INSTALL_DIRECTORY/.yatm-upgrades" attempt entry report recognized
  [[ "$BACKUP_COMPLETE" == 1 && -d "$old" && ! -L "$old" ]] || return 0
  [[ -f "$old/OWNER" && ! -L "$old/OWNER" && "$(< "$old/OWNER")" == yatm-installer-upgrades ]] || {
    echo "Preserved unrecognized historical directory: $old"; return 0;
  }
  # Ownership comes from the known layout and log, not the success of an old backup.
  # Cancelled and failed attempts are safe to remove once the new whole-tree backup is verified.
  while IFS= read -r -d '' attempt; do
    [[ "${attempt##*/}" =~ ^[0-9]{14}\.[a-zA-Z0-9]{6}$ && -d "$attempt" && ! -L "$attempt" ]] || continue
    [[ -d "$attempt/reports" && ! -L "$attempt/reports" && \
      -f "$attempt/reports/upgrade.log" && ! -L "$attempt/reports/upgrade.log" ]] || continue
    recognized=1
    while IFS= read -r -d '' entry; do
      case "${entry##*/}" in
        reports|release) [[ -d "$entry" && ! -L "$entry" ]] || recognized=0 ;;
        v*.backup) [[ "${entry##*/}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-z]+\.[0-9]+)?\.backup$ && -d "$entry" && ! -L "$entry" ]] || recognized=0 ;;
        *) recognized=0 ;;
      esac
    done < <(find "$attempt" -mindepth 1 -maxdepth 1 -print0)
    while IFS= read -r -d '' report; do
      case "${report##*/}" in
        backup.status)
          if [[ ! -f "$report" || -L "$report" ]]; then
            recognized=0
          elif [[ "$(< "$report")" != complete && "$(< "$report")" != incomplete ]]; then
            recognized=0
          fi ;;
        upgrade.log|preflight.json|release.sha256|backup.entries|migration.json)
          [[ -f "$report" && ! -L "$report" ]] || recognized=0 ;;
        *) recognized=0 ;;
      esac
    done < <(find "$attempt/reports" -mindepth 1 -maxdepth 1 -print0)
    if [[ "$recognized" == 1 ]]; then
      rm -rf -- "$attempt"
    else
      echo "Preserved unrecognized historical attempt: $attempt"
    fi
  done < <(find "$old" -mindepth 1 -maxdepth 1 -print0)
  if [[ -z "$(find "$old" -mindepth 1 -maxdepth 1 ! -name OWNER ! -name lock -print -quit)" && \
    ! -L "$old/lock" && ( ! -e "$old/lock" || -f "$old/lock" ) ]]; then
    rm -f -- "$old/OWNER" "$old/lock"
    rmdir "$old"
  else
    echo "Preserved historical artifacts requiring manual review: $old"
  fi
}

reverse_prepare() {
  if run_migrator -phase abort --confirm; then
    STAGE=reversed
    INSTALLATION_CHANGED=0
    if [[ "$SERVICE_ACTIVE" == 1 ]]; then systemctl start "$SERVICE_NAME"; fi
    SERVICE_STOPPED=0
    echo 'Prepared migration aborted; original service state restored.'
    return 0
  fi
  echo 'Abort failed. Keep YATM stopped and recover the complete backup.' >&2
  return 1
}

restore_previous_service() {
  [[ "$SERVICE_ACTIVE" == 1 ]] || return 0
  if ! systemctl restart "$SERVICE_NAME"; then
    fail 'The previous service could not be restarted; keep the installation unchanged and recover it manually.'
    return 1
  fi
  local attempt recovered=0
  for attempt in {1..15}; do
    if systemctl is-active --quiet "$SERVICE_NAME" && \
      { [[ "$SCHEMA" != current ]] || ready_process_id >/dev/null; }; then
      recovered=1
      break
    fi
    sleep 1
  done
  [[ "$recovered" == 1 ]] || {
    fail 'The restarted service did not recover its active state and local admission endpoint.'
    return 1
  }
  echo 'Previous service admission and active state restored.'
  SERVICE_STOPPED=0
}

quiesce_and_stop() {
  [[ "$SERVICE_ACTIVE" == 1 ]] || return 0
  local expected output
  expected="$(service_main_pid)" || { fail 'The active systemd service has no stable MainPID.'; return 1; }
  if ! output="$(run_migrator -phase quiesce --confirm -install-root "$INSTALL_DIRECTORY" -service-pid "$expected" 2>&1)"; then
    printf '%s\n' "$output" >&2
    if [[ "$output" != *'running Jobs must finish'* ]]; then
      restore_previous_service || true
    fi
    return 1
  fi
  printf '%s\n' "$output"
  if [[ "$(service_main_pid)" != "$expected" ]]; then
    restore_previous_service || true
    fail 'The systemd service changed while quiescing.'
    return 1
  fi

  STAGE=stopping
  if ! systemctl stop "$SERVICE_NAME"; then
    if systemctl is-active --quiet "$SERVICE_NAME"; then
      restore_previous_service || true
      fail 'Stopping the service failed; its admission state was restored if possible.'
      return 1
    fi
  fi
  SERVICE_STOPPED=1
}

complete_legacy_migration() {
  STAGE=commit
  run_migrator -phase commit --confirm
  # Only the locally generated and verified archive is extracted for legacy evidence.
  local evidence="$WORK_DIRECTORY/backup"
  mkdir -m 700 "$evidence"
  (cd "$BACKUP_DIRECTORY" && sha256sum --check yatm.tar.gz.sha256)
  tar --acls --xattrs --xattrs-include='*' --numeric-owner -xzf "$BACKUP_DIRECTORY/yatm.tar.gz" -C "$evidence"
  # Cleanup fully validates the migration against the backup before removing obsolete artifacts.
  STAGE=cleanup
  run_migrator -phase cleanup -backup-root "$evidence" -install-root "$INSTALL_DIRECTORY" --confirm
}

upgrade_existing() {
  # Recheck after consent, then close current admission at the actual activation boundary.
  if [[ "$SERVICE_ACTIVE" == 1 ]]; then
    quiesce_and_stop
  else
    run_migrator -phase quiesce --confirm -install-root "$INSTALL_DIRECTORY" -service-stopped
  fi
  STAGE=config-check
  run_migrator -phase config-check -plan-file "$CONFIG_PLAN" -service-stopped
  if ! backup_installation; then
    restore_previous_service || true
    return 1
  fi
  if [[ "$SCHEMA" == legacy ]]; then
    STAGE=prepare
    INSTALLATION_CHANGED=1
    if ! run_migrator -phase prepare -report-file "$REPORT_DIRECTORY/migration.json"; then
      reverse_prepare
      return 1
    fi
    if ! confirm_action 'Approve this migration report and commit the migration?'; then
      reverse_prepare
      exit 0
    fi
    complete_legacy_migration
  fi
  if [[ "$CONFIG_CHANGED" == true ]]; then
    STAGE=config-apply
    INSTALLATION_CHANGED=1
    run_migrator -phase config-apply -plan-file "$CONFIG_PLAN" -service-stopped --confirm
  fi
}

install_managed_files() {
  # This allowlist does not overwrite active configuration, scripts or unknown local files.
  STAGE=replace-programs
  INSTALLATION_CHANGED=1
  mkdir -p "$INSTALL_DIRECTORY"
  local item
  for item in "${MANAGED_ITEMS[@]}"; do
    if [[ ! -e "$RELEASE_DIRECTORY/$item" ]]; then
      [[ "$LEGACY" == 1 ]] || { fail "Candidate is missing managed resource $item."; return 1; }
      continue
    fi
    # Replace owned trees in full so obsolete release files cannot survive an overlay.
    rm -rf -- "$INSTALL_DIRECTORY/$item"
    cp -a "$RELEASE_DIRECTORY/$item" "$INSTALL_DIRECTORY/"
  done
  if [[ "$CURRENT_VERSION" != none ]]; then
    [[ "$BACKUP_COMPLETE" == 1 ]] || { fail 'Owned obsolete files may only be removed after a complete backup.'; return 1; }
    for item in "${OBSOLETE_MANAGED_ITEMS[@]}"; do
      rm -rf -- "$INSTALL_DIRECTORY/$item"
    done
  fi
  local templates="$RELEASE_DIRECTORY/templates"
  [[ "$LEGACY" == 0 ]] || templates="$RELEASE_DIRECTORY"
  if [[ "$CURRENT_VERSION" == none ]]; then
    cp "$INSPECTION_DIRECTORY/config.yaml" "$INSTALL_DIRECTORY/config.yaml"
    cp -a "$templates/scripts" "$INSTALL_DIRECTORY/"
    sed "s|/opt/yatm|$INSTALL_DIRECTORY|g" "$templates/yatm-httpd.service" > "$INSTALL_DIRECTORY/$SERVICE_NAME"
  fi
  [[ -f "$INSTALL_DIRECTORY/$SERVICE_NAME" ]] || { fail 'Missing preserved systemd unit.'; return 1; }
}

managed_install_matches_candidate() {
  local installed candidate installed_version item
  for item in "${OBSOLETE_MANAGED_ITEMS[@]}"; do
    [[ ! -e "$INSTALL_DIRECTORY/$item" && ! -L "$INSTALL_DIRECTORY/$item" ]] || return 1
  done
  [[ -f "$INSTALL_DIRECTORY/VERSION" && -f "$INSTALL_DIRECTORY/COMMIT" ]] || return 1
  installed_version="$(< "$INSTALL_DIRECTORY/VERSION")"
  [[ "v${installed_version#v}" == "v${RELEASE_VERSION#v}" ]] || return 1
  [[ "$(< "$INSTALL_DIRECTORY/COMMIT")" == "$(< "$RELEASE_DIRECTORY/COMMIT")" ]] || return 1
  installed="$(cd "$INSTALL_DIRECTORY" && tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner -cf - "${MANAGED_ITEMS[@]}" 2>/dev/null | sha256sum)" || return 1
  candidate="$(cd "$RELEASE_DIRECTORY" && tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner -cf - "${MANAGED_ITEMS[@]}" 2>/dev/null | sha256sum)" || return 1
  [[ "${installed%% *}" == "${candidate%% *}" ]]
}

ready_process_id() {
  local expected="${1:-}" actual
  actual="$(systemctl show "$SERVICE_NAME" --property MainPID --value)" || return 1
  [[ "$actual" =~ ^[1-9][0-9]*$ && ( -z "$expected" || "$actual" == "$expected" ) ]] || return 1
  systemctl is-active --quiet "$SERVICE_NAME" || return 1
  command curl -q --noproxy '*' --fail --silent --show-error --connect-timeout 2 --max-time 2 \
    "$SERVER_URL/files/_upgrade/status" | jq -e --argjson pid "$actual" '.process_id == $pid' >/dev/null || return 1
  [[ "$(systemctl show "$SERVICE_NAME" --property MainPID --value)" == "$actual" ]] || return 1
  systemctl is-active --quiet "$SERVICE_NAME" || return 1
  printf '%s\n' "$actual"
}

check_readiness() {
  local attempt process_id=
  if [[ "$LEGACY" == 1 ]]; then
    systemctl is-active --quiet "$SERVICE_NAME"
    echo 'Legacy release started; validate its UI before use. This release has no CLI readiness probe.'
    return
  fi
  for attempt in {1..15}; do
    if process_id="$(ready_process_id 2>/dev/null)"; then break; fi
    sleep 1
  done
  [[ -n "$process_id" ]] || { fail 'Readiness endpoint does not belong to the running systemd service.'; return 1; }
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s status
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s ls > "$TMP_DIRECTORY/library.json"
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s job list --limit 1 > "$TMP_DIRECTORY/jobs.json"
  (cd "$INSTALL_DIRECTORY" && ./yatm-migrate -config ./config.yaml -phase frontend-check)
  ready_process_id "$process_id" >/dev/null || { fail 'The systemd service changed or stopped during readiness checks.'; return 1; }
  if [[ "$INSTALL_PREVIEW" == 1 ]]; then
    "$INSTALL_DIRECTORY/yatm-preview" --capabilities | jq -e --arg version "$RELEASE_VERSION" \
      '.protocol == 1 and .type == "capabilities" and .capabilities.version == $version' >/dev/null
  fi
}

offer_skill() {
  local skill_path="$INSTALL_DIRECTORY/skills/yatm" skill_user="${SUDO_USER:-$(id -un)}"
  local -a command_prefix=()
  [[ -f "$skill_path/SKILL.md" ]] || { echo 'Optional Skill: this release does not include one.'; return 0; }
  if [[ -z "$skill_user" || "$skill_user" == root ]] || ! id "$skill_user" >/dev/null 2>&1; then
    echo "Optional Skill: run as your ordinary user: DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 npx --yes --registry=https://registry.npmjs.org skills@$SKILLS_VERSION add '$skill_path' --global --copy"
    return 0
  fi
  if [[ "$(id -un)" != "$skill_user" ]]; then command_prefix=(sudo -H -u "$skill_user"); fi
  if ! "${command_prefix[@]}" sh -c 'command -v node >/dev/null && command -v npx >/dev/null'; then
    echo "Optional Skill skipped: Node/npm are not available for $skill_user. They were not installed."
    return 0
  fi
  if [[ ! -t 0 ]]; then
    echo "Optional Skill: run as $skill_user: DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 npx --yes --registry=https://registry.npmjs.org skills@$SKILLS_VERSION add '$skill_path' --global --copy"
    return 0
  fi
  confirm_action "Install this release's YATM Skill for $skill_user? Review agents and confirm in the Skills CLI." || return 0
  # sudo changes identity, not the caller's directory (which may be an unreadable /root).
  if ! "${command_prefix[@]}" sh -c 'cd && exec "$@"' sh env DISABLE_TELEMETRY=1 DO_NOT_TRACK=1 \
    npx --yes --registry=https://registry.npmjs.org "skills@$SKILLS_VERSION" add "$skill_path" --global --copy; then
    echo 'Optional Skill installation cancelled or failed. YATM installation remains successful.'
  fi
}

on_failure() {
  local status="$1"
  trap - ERR
  if [[ "$INSTALLATION_CHANGED" == 1 ]]; then
    systemctl stop "$SERVICE_NAME" || true
  elif [[ "$SERVICE_STOPPED" == 1 ]]; then
    restore_previous_service || true
  fi
  echo "Installation did not complete. Stage: $STAGE" >&2
  if [[ -n "$BACKUP_DIRECTORY" && "$BACKUP_COMPLETE" == 1 ]]; then
    echo "Complete backup: $BACKUP_DIRECTORY. Recover the complete installation, not only its executables." >&2
  elif [[ -n "$BACKUP_DIRECTORY" ]]; then
    echo "Incomplete backup: $BACKUP_DIRECTORY. Do not use it as a complete recovery source." >&2
  fi
  [[ -z "$REPORT_FILE" ]] || echo "Report: $REPORT_FILE" >&2
  exit "$status"
}

main() {
  parse_options "$@"
  if [[ "$SHOW_HELP" == 1 ]]; then usage; return; fi
  identify_platform
  TMP_DIRECTORY="$(mktemp -d -t yatm-install.XXXXXX)"
  RELEASE_DIRECTORY="$TMP_DIRECTORY/release"
  INITIAL_SCRATCH="$TMP_DIRECTORY"
  trap '[[ -z "${INITIAL_SCRATCH:-}" ]] || rm -rf -- "$INITIAL_SCRATCH"' EXIT
  trap 'on_failure "$?"' ERR
  trap 'on_failure 130' INT TERM
  reject_ambiguous_fresh_root
  reject_pending_migration
  resolve_version
  if [[ "$CURRENT_VERSION" == none ]]; then
    verify_service_ownership
  fi
  download_release
  prepare_preview
  inspect_installation
  # Reject unsafe backup layouts before creating reports or locks inside the installation.
  # Keep verified inputs in the retained work directory after acquiring the installer lock.
  local inspected_directory="$TMP_DIRECTORY"
  begin_attempt
  if [[ "$CHECK_ONLY" == 0 ]]; then
    cp -a "$inspected_directory/." "$WORK_DIRECTORY/"
    if [[ "$CURRENT_VERSION" == none ]]; then INSPECTION_DIRECTORY="$WORK_DIRECTORY/fresh"; fi
  fi
  show_migration_guide
  plan_configuration
  if [[ "$CHECK_ONLY" == 1 ]]; then echo 'Read-only installation checks passed; review any manual checks above. No service or installation changes were made.'; return; fi
  if [[ "$CURRENT_VERSION" == "$RELEASE_VERSION" && "$CONFIG_CHANGED" == false ]] && \
    { [[ "$LEGACY" == 1 ]] || managed_install_matches_candidate; }; then
    if [[ "$LEGACY" == 0 ]]; then
      local program
      for program in yatm-httpd yatm-cli yatm-migrate yatm-export-library yatm-lto-info; do
        "$INSTALL_DIRECTORY/$program" --version | jq -e --arg program "$program" --arg version "$RELEASE_VERSION" \
          --arg commit "$(< "$RELEASE_DIRECTORY/COMMIT")" '.program == $program and .version == $version and .commit == $commit' >/dev/null
      done
      STAGE=readiness
      if [[ "$SERVICE_ACTIVE" == 0 ]]; then systemctl start "$SERVICE_NAME"; fi
      check_readiness
    fi
    echo "YATM $RELEASE_VERSION is already installed; no replacement is needed."
    offer_skill
    clean_work_directory
    return
  fi
  echo "Install $RELEASE_VERSION (currently $CURRENT_VERSION). Review configuration changes above; local scripts and files are retained."
  if [[ "$CURRENT_VERSION" != none ]]; then
    echo "The service will stop after confirmation. Its complete backup will remain inside $ATTEMPT_DIRECTORY."
  fi
  confirm_action 'Continue with installation?' || { echo 'Installation cancelled; existing service and data are unchanged.'; clean_work_directory; return; }
  if [[ "$CURRENT_VERSION" != none ]]; then upgrade_existing; fi
  install_managed_files
  STAGE=readiness
  systemctl daemon-reload
  systemctl enable "$INSTALL_DIRECTORY/$SERVICE_NAME"
  systemctl start "$SERVICE_NAME"
  check_readiness
  clean_old_upgrades
  clean_work_directory
  STAGE=complete
  echo "Installed $RELEASE_VERSION."
  if [[ "$LEGACY" == 1 ]]; then
    echo "UI: use the address configured in $INSTALL_DIRECTORY/config.yaml."
  else
    echo "UI: $SERVER_URL"
  fi
  echo "Configuration: $INSTALL_DIRECTORY/config.yaml"
  echo "Backup: ${BACKUP_DIRECTORY:-not needed for a fresh installation}"
  echo "Report: $REPORT_FILE"
  echo 'Before the first physical Tape Job, validate the preserved customized Tape scripts for this environment.'
  offer_skill
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi

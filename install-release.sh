#!/usr/bin/env bash
set -Eeuo pipefail

INSTALL_DIRECTORY=/opt/yatm
SERVICE_NAME=yatm-httpd.service
SKILLS_VERSION=1.5.0
STAGE=preflight
BACKUP_DIRECTORY=
REPORT_FILE=
SERVICE_ACTIVE=0
RELEASE_VERSION=
LOCAL_ARCHIVE=
CHECKSUM_FILE=
FRESH_CONFIG=
CHECK_ONLY=0
ADOPT_SCRIPTS=0
ADOPT_UNIT=0
LEGACY=0

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
  echo 'Linux amd64/systemd only. Requires curl, jq, tar and sha256sum.'
  echo 'Stable is the default. Alpha and local candidates require an explicit version.'
  echo '--check performs read-only checks; it does not stop or quiesce YATM.'
  echo '--config supplies the configuration for a fresh installation only.'
}

parse_options() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) RELEASE_VERSION="v${2#v}"; shift 2 ;;
      --archive) LOCAL_ARCHIVE="${2:?Missing archive}"; shift 2 ;;
      --checksum) CHECKSUM_FILE="${2:?Missing checksum}"; shift 2 ;;
      --install-dir) INSTALL_DIRECTORY="${2:?Missing installation directory}"; shift 2 ;;
      --service) SERVICE_NAME="${2:?Missing service name}"; shift 2 ;;
      --config) FRESH_CONFIG="${2:?Missing configuration}"; shift 2 ;;
      --check) CHECK_ONLY=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) fail "Unknown option: $1"; return 1 ;;
    esac
  done
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
  for dependency in curl jq tar sha256sum systemctl cp du df readlink mktemp; do
    command -v "$dependency" >/dev/null || { fail "Install the required tool '$dependency' first."; return 1; }
  done
  [[ "$INSTALL_DIRECTORY" =~ ^/[a-zA-Z0-9_./-]+$ && "$INSTALL_DIRECTORY" != */../* && "$INSTALL_DIRECTORY" != */.. ]] || {
    fail 'Installation directory must be an absolute path without whitespace or parent traversal.'; return 1;
  }
  INSTALL_DIRECTORY="$(readlink -m "$INSTALL_DIRECTORY")"
  case "$INSTALL_DIRECTORY" in /|/opt|/usr|/usr/local|/var|/tmp|/home|/root) fail 'Choose a dedicated YATM installation directory.'; return 1 ;; esac
  [[ "$SERVICE_NAME" =~ ^[a-zA-Z0-9_-]+\.service$ ]] || { fail 'Invalid systemd service name.'; return 1; }
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
    echo "warning: Recognized legacy legacy release $RELEASE_VERSION has no publisher checksum. Source: $url"
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
  tar -xzf "$ARCHIVE" --no-same-owner -C "$RELEASE_DIRECTORY"
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

prepare_fresh_config() {
  INSPECTION_DIRECTORY="$TMP_DIRECTORY/fresh"
  mkdir "$INSPECTION_DIRECTORY"
  local templates="$RELEASE_DIRECTORY/templates"
  [[ "$LEGACY" == 0 ]] || templates="$RELEASE_DIRECTORY"
  cp -a "$templates/scripts" "$INSPECTION_DIRECTORY/scripts"
  cp "${FRESH_CONFIG:-$templates/config.example.yaml}" "$INSPECTION_DIRECTORY/config.yaml"
  if [[ -z "$FRESH_CONFIG" && "$CHECK_ONLY" == 0 ]]; then "${EDITOR:-vi}" "$INSPECTION_DIRECTORY/config.yaml"; fi
}

inspect_installation() {
  INSPECTION_DIRECTORY="$INSTALL_DIRECTORY"
  if [[ "$CURRENT_VERSION" == none ]]; then
    prepare_fresh_config
  else
    [[ -z "$FRESH_CONFIG" ]] || { fail '--config is only for a fresh installation; existing configuration is preserved.'; return 1; }
    if systemctl is-active --quiet "$SERVICE_NAME"; then SERVICE_ACTIVE=1; fi
    local fragment working_directory
    fragment="$(systemctl show "$SERVICE_NAME" --property FragmentPath --value)"
    if [[ -n "$fragment" && "$(readlink -f "$fragment")" != "$INSTALL_DIRECTORY/$SERVICE_NAME" ]]; then
      fail "The service unit is outside the installation backup: $fragment. Use manual upgrade."; return 1
    fi
    working_directory="$(systemctl show "$SERVICE_NAME" --property WorkingDirectory --value)"
    if [[ -n "$working_directory" && "$(readlink -m "$working_directory")" != "$INSTALL_DIRECTORY" ]]; then
      fail 'The configured service working directory differs from the installation. Use manual upgrade.'; return 1
    fi
  fi
  if [[ "$LEGACY" == 1 ]]; then
    [[ "$CURRENT_VERSION" == none || "$CURRENT_VERSION" == "$RELEASE_VERSION" ]] || {
      fail 'This legacy candidate has no read-only migration inspector. Use the legacy manual update guide or explicitly select current Alpha.'; return 1;
    }
    return
  fi
  local stopped=()
  [[ "$SERVICE_ACTIVE" == 1 ]] || stopped=(-service-stopped)
  run_migrator -phase preflight -json -install-root "$INSPECTION_DIRECTORY" "${stopped[@]}" > "$TMP_DIRECTORY/preflight.json"
  jq . "$TMP_DIRECTORY/preflight.json"
  SCHEMA="$(jq -er .schema "$TMP_DIRECTORY/preflight.json")"
  SERVER_URL="$(jq -er .server_url "$TMP_DIRECTORY/preflight.json")"
  if [[ "$CURRENT_VERSION" != none ]]; then
    local needed available
    needed="$(du -sk "$INSTALL_DIRECTORY" | awk '{print $1}')"
    available="$(df -Pk "$(dirname "$INSTALL_DIRECTORY")" | awk 'END {print $4}')"
    (( available > needed + 10240 )) || { fail 'Insufficient free space for a complete installation backup plus safety margin.'; return 1; }
  fi
}

choose_templates() {
  [[ "$CURRENT_VERSION" != none && "$LEGACY" == 0 ]] || return 0
  if [[ "$SCHEMA" == v1 && "$(jq '.warnings | length' "$TMP_DIRECTORY/preflight.json")" != 0 ]]; then
    if [[ "$(jq .standard_scripts "$TMP_DIRECTORY/preflight.json")" == true ]] && confirm_action 'Adopt the bundled current Tape script templates after migration? Existing scripts remain in the complete backup.'; then
      ADOPT_SCRIPTS=1
    else
      confirm_action 'I have adapted the retained custom Tape scripts to the current TAPE_DIR/index and unmount-completion contract. Continue?' || return 1
    fi
  fi
  if confirm_action 'Replace the service unit with the bundled template? Default is to preserve the existing unit.'; then ADOPT_UNIT=1; fi
  return 0
}

backup_installation() {
  STAGE=backup
  BACKUP_DIRECTORY="$(mktemp -d "${INSTALL_DIRECTORY}.bak.$(date +%Y%m%d%H%M%S).XXXXXX")"
  echo "Complete installation backup: $BACKUP_DIRECTORY"
  cp -a "$INSTALL_DIRECTORY/." "$BACKUP_DIRECTORY/"
  echo 'Backup complete.'
}

reverse_prepare() {
  if run_migrator -phase abort --confirm; then
    STAGE=reversed
    if [[ "$SERVICE_ACTIVE" == 1 ]]; then systemctl start "$SERVICE_NAME"; fi
    echo 'Prepared migration aborted; original service state restored.'
    return 0
  fi
  echo 'Abort failed. Keep YATM stopped and recover the complete backup.' >&2
  return 1
}

upgrade_existing() {
  # Recheck after consent, then close current admission at the actual activation boundary.
  local stopped=()
  [[ "$SERVICE_ACTIVE" == 1 ]] || stopped=(-service-stopped)
  run_migrator -phase quiesce --confirm -install-root "$INSTALL_DIRECTORY" "${stopped[@]}"
  STAGE=stopping
  systemctl stop "$SERVICE_NAME"
  backup_installation
  if [[ "$SCHEMA" == v1 ]]; then
    STAGE=prepare
    if ! run_migrator -phase prepare; then
      reverse_prepare
      return 1
    fi
    if ! confirm_action 'Approve this complete migration report and commit current?'; then
      reverse_prepare
      exit 0
    fi
    STAGE=commit
    run_migrator -phase commit --confirm
    echo 'legacy backup tables are retained. Cleanup remains a separate, explicitly confirmed operation.'
  fi
}

install_managed_files() {
  # This allowlist does not overwrite active configuration, scripts or unknown local files.
  STAGE=replace-programs
  mkdir -p "$INSTALL_DIRECTORY"
  local item
  for item in yatm-httpd yatm-cli yatm-export-library yatm-lto-info yatm-migrate frontend README.md CONTEXT.md docs VERSION COMMIT LICENSE licenses skills templates; do
    [[ ! -e "$RELEASE_DIRECTORY/$item" ]] || cp -a "$RELEASE_DIRECTORY/$item" "$INSTALL_DIRECTORY/"
  done
  local templates="$RELEASE_DIRECTORY/templates"
  [[ "$LEGACY" == 0 ]] || templates="$RELEASE_DIRECTORY"
  if [[ "$CURRENT_VERSION" == none ]]; then
    cp "$INSPECTION_DIRECTORY/config.yaml" "$INSTALL_DIRECTORY/config.yaml"
    ADOPT_SCRIPTS=1
    ADOPT_UNIT=1
  fi
  if [[ "$ADOPT_SCRIPTS" == 1 ]]; then cp -a "$templates/scripts" "$INSTALL_DIRECTORY/"; fi
  if [[ "$ADOPT_UNIT" == 1 ]]; then
    sed "s|/opt/yatm|$INSTALL_DIRECTORY|g" "$templates/yatm-httpd.service" > "$INSTALL_DIRECTORY/$SERVICE_NAME"
  fi
  [[ -f "$INSTALL_DIRECTORY/$SERVICE_NAME" ]] || { fail 'Missing preserved systemd unit.'; return 1; }
}

check_readiness() {
  local attempt
  if [[ "$LEGACY" == 1 ]]; then
    systemctl is-active --quiet "$SERVICE_NAME"
    echo 'Legacy legacy started; validate its UI before use. This release has no current CLI readiness probe.'
    return
  fi
  for attempt in {1..15}; do
    if "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 2s status > "$TMP_DIRECTORY/status.json" 2>/dev/null; then break; fi
    sleep 1
  done
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s status
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s files list --file-id 0 --limit 1 > "$TMP_DIRECTORY/library.json"
  "$INSTALL_DIRECTORY/yatm-cli" --server "$SERVER_URL" --timeout 3s job list --limit 1 > "$TMP_DIRECTORY/jobs.json"
  (cd "$INSTALL_DIRECTORY" && ./yatm-migrate -config ./config.yaml -phase frontend-check)
  systemctl is-active --quiet "$SERVICE_NAME"
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
  case "$STAGE" in commit|replace-programs|readiness) systemctl stop "$SERVICE_NAME" || true ;; esac
  echo "Installation did not complete. Stage: $STAGE" >&2
  [[ -z "$BACKUP_DIRECTORY" ]] || echo "Backup: $BACKUP_DIRECTORY. Recover the complete installation, not only its executables." >&2
  [[ -z "$REPORT_FILE" ]] || echo "Report: $REPORT_FILE" >&2
  exit "$status"
}

main() {
  parse_options "$@"
  identify_platform
  TMP_DIRECTORY="$(mktemp -d -t yatm-install.XXXXXX)"
  RELEASE_DIRECTORY="$TMP_DIRECTORY/release"
  trap '[[ -z "${TMP_DIRECTORY:-}" ]] || rm -rf -- "$TMP_DIRECTORY"' EXIT
  trap 'on_failure "$?"' ERR
  trap 'on_failure 130' INT TERM
  resolve_version
  download_release
  inspect_installation
  if [[ "$CHECK_ONLY" == 1 ]]; then echo 'Read-only installation checks passed. No service or installation changes were made.'; return; fi
  if [[ "$CURRENT_VERSION" == "$RELEASE_VERSION" ]]; then
    if [[ "$LEGACY" == 0 ]]; then
      local program
      for program in yatm-httpd yatm-cli yatm-migrate yatm-export-library yatm-lto-info; do
        "$INSTALL_DIRECTORY/$program" --version | jq -e --arg program "$program" --arg version "$RELEASE_VERSION" \
          --arg commit "$(< "$INSTALL_DIRECTORY/COMMIT")" '.program == $program and .version == $version and .commit == $commit' >/dev/null
      done
      if [[ "$SERVICE_ACTIVE" == 1 ]]; then check_readiness; fi
    fi
    echo "YATM $RELEASE_VERSION is already installed; no replacement is needed."
    offer_skill
    return
  fi
  echo "Install $RELEASE_VERSION (currently $CURRENT_VERSION). Existing configuration and local files are retained."
  if [[ "$CURRENT_VERSION" != none ]]; then
    echo "The service will stop. A complete backup will be created beside $INSTALL_DIRECTORY. legacy migration changes the database and requires current afterward."
  fi
  confirm_action 'Continue with installation?' || { echo 'Installation cancelled; existing service and data are unchanged.'; return; }
  choose_templates || { echo 'Installation cancelled; existing service and data are unchanged.'; return; }
  REPORT_FILE="$(mktemp "${INSTALL_DIRECTORY}.upgrade.$(date +%Y%m%d%H%M%S).XXXXXX.log")"
  exec > >(tee -a "$REPORT_FILE") 2>&1
  if [[ "$CURRENT_VERSION" != none ]]; then upgrade_existing; fi
  install_managed_files
  STAGE=readiness
  systemctl daemon-reload
  systemctl enable "$INSTALL_DIRECTORY/$SERVICE_NAME"
  systemctl start "$SERVICE_NAME"
  check_readiness
  STAGE=complete
  echo "Installed $RELEASE_VERSION. Backup: ${BACKUP_DIRECTORY:-not needed for a fresh installation}. Report: $REPORT_FILE"
  offer_skill
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi

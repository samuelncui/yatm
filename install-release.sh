#!/usr/bin/env bash

# Inspired by:
# https://github.com/v2fly/fhs-install-v2ray

# The URL of the script is:
# https://raw.githubusercontent.com/samuelncui/yatm/main/install-release.sh

# Use following command to run:
# bash <(curl -L https://raw.githubusercontent.com/samuelncui/yatm/main/install-release.sh) 

curl() {
  $(type -P curl) -L -q --retry 5 --retry-delay 10 --retry-max-time 60 "$@"
}

identify_the_operating_system_and_architecture() {
  if [[ "$(uname)" == 'Linux' ]]; then
    case "$(uname -m)" in
      'amd64' | 'x86_64')
        MACHINE='amd64'
        ;;
      *)
        echo "error: The architecture is not supported."
        exit 1
        ;;
    esac
  else
    echo "error: This operating system is not supported."
    exit 1
  fi
}

judgment_parameters() {
  while [[ "$#" -gt '0' ]]; do
    case "$1" in
      '--version')
        if [[ "$#" -lt 2 || "$2" == --* || -z "$2" ]]; then
          echo 'error: --version requires a version.' >&2
          return 1
        fi
        VERSION="$2"
        shift 2
        ;;
      '--version='*)
        echo 'error: Use --version followed by a version.' >&2
        return 1
        ;;
      '-h' | '--help')
        HELP='1'
        shift
        ;;
      *)
        echo "error: Unknown option: $1" >&2
        return 1
        ;;
    esac
  done
}

validate_legacy_upgrade() {
  if [[ ! "$RELEASE_VERSION" =~ ^v0\.1\.[0-9]+$ ]]; then
    echo 'error: This installer supports v0.1.x only. Use the installer supplied with the target release.' >&2
    echo 'Upgrade guide: https://github.com/samuelncui/yatm/blob/v1/docs/operations/migration.md' >&2
    return 1
  fi
  if [[ -n "$CURRENT_VERSION" && ! "$CURRENT_VERSION" =~ ^v0\.1\.[0-9]+$ ]]; then
    echo "error: Cannot update installed version '$CURRENT_VERSION' with the v0.1.x installer." >&2
    return 1
  fi
}

get_current_version() {
  CURRENT_VERSION=`cat /opt/yatm/VERSION`
}

get_version() {
  if [[ -n "$VERSION" ]]; then
    RELEASE_VERSION="v${VERSION#v}"
    return 2
  fi

  # Determine the version number for YATM installed from a local file
  if [[ -f '/opt/yatm/VERSION' ]]; then
    get_current_version
  fi

  # Get YATM release version number
  TMP_FILE="$(mktemp)"
  if ! curl -x "${PROXY}" -sS -i -H "Accept: application/vnd.github.v3+json" -o "$TMP_FILE" 'https://api.github.com/repos/samuelncui/yatm/releases/latest'; then
    "rm" "$TMP_FILE"
    echo 'error: Failed to get release list, please check your network.'
    exit 1
  fi

  HTTP_STATUS_CODE=$(awk 'NR==1 {print $2}' "$TMP_FILE")
  if [[ $HTTP_STATUS_CODE -lt 200 ]] || [[ $HTTP_STATUS_CODE -gt 299 ]]; then
    "rm" "$TMP_FILE"
    echo "error: Failed to get release list, GitHub API response code: $HTTP_STATUS_CODE"
    exit 1
  fi

  RELEASE_LATEST="$(sed 'y/,/\n/' "$TMP_FILE" | grep 'tag_name' | awk -F '"' '{print $4}')"
  "rm" "$TMP_FILE"
  RELEASE_VERSION="v${RELEASE_LATEST#v}"

  # Compare YATM version numbers
  if [[ "$RELEASE_VERSION" != "$CURRENT_VERSION" ]]; then
    return 0
  fi

  return 1
}

download_yatm() {
  DOWNLOAD_LINK="https://github.com/samuelncui/yatm/releases/download/$RELEASE_VERSION/yatm-linux-$MACHINE-$RELEASE_VERSION.tar.gz"

  echo "Downloading YATM archive: $DOWNLOAD_LINK"
  if ! curl -x "${PROXY}" -R -H 'Cache-Control: no-cache' -o "$GZIP_FILE" "$DOWNLOAD_LINK"; then
    echo 'error: Download failed! Please check your network or try again.'
    return 1
  fi
}

# Explanation of parameters in the script
show_help() {
  echo "usage:"
  echo '  --version       Install the specified version of YATM, e.g., --version v0.1.0'
  echo '  -h, --help      Show help'
  exit 0
}

main() {
  judgment_parameters "$@" || return 1

  # Parameter information
  [[ "$HELP" -eq '1' ]] && show_help
  identify_the_operating_system_and_architecture
  [[ ! -f '/opt/yatm/VERSION' ]] || get_current_version

  # Two very important variables
  TMP_DIRECTORY="$(mktemp -d)"

  get_version
  NUMBER="$?"
  if ! validate_legacy_upgrade; then
    rmdir "$TMP_DIRECTORY"
    return 1
  fi
  if [[ "$NUMBER" -eq '1' ]]; then
    echo "info: No new version. The current version of YATM is $CURRENT_VERSION."
    exit 0
  fi

  echo "info: Installing YATM $RELEASE_VERSION for $(uname -m)"
  GZIP_FILE="${TMP_DIRECTORY}/yatm-linux-$MACHINE-$RELEASE_VERSION.tar.gz"

  download_yatm
  if [[ "$?" -eq '1' ]]; then
    "rm" -r "$TMP_DIRECTORY"
    echo "removed: $TMP_DIRECTORY"
    exit 1
  fi

  mkdir -p /opt/yatm
  tar -xvzf ${GZIP_FILE} -C /opt/yatm

  if [[ ! -f '/opt/yatm/config.yaml' ]]; then
    cp /opt/yatm/config.example.yaml /opt/yatm/config.yaml;
    vim /opt/yatm/config.yaml;
    echo "Copy example config to /opt/yatm/config.yaml, you may edit it later";
  fi

  systemctl daemon-reload
  systemctl enable /opt/yatm/yatm-httpd.service
  systemctl stop yatm-httpd.service
  systemctl start yatm-httpd.service
  systemctl status yatm-httpd.service
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

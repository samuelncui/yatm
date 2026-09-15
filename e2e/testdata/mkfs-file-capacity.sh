#!/usr/bin/env bash
set -euo pipefail

export PATH=/usr/local/bin:/usr/bin:/bin
rm -rf -- "${DEVICE}"
mkdir -p -- "${DEVICE}"
cat >"${DEVICE}/filedebug_tc_conf.xml" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<filedebug_cartridge_config>
    <dummy_io>false</dummy_io>
    <emulate_readonly>false</emulate_readonly>
    <capacity_mb>${YATM_E2E_TAPE_CAPACITY_MB:-3072}</capacity_mb>
    <cart_type>L5</cart_type>
    <density_code>58</density_code>
    <delay_mode>None</delay_mode>
    <wraps>40</wraps>
    <eot_to_bot_sec>12</eot_to_bot_sec>
    <change_direction_us>2000000</change_direction_us>
    <change_track_us>10000</change_track_us>
    <threading_sec>0</threading_sec>
</filedebug_cartridge_config>
EOF
mkltfs -f -e file -d "${DEVICE}" -s "${TAPE_BARCODE}" -n "${TAPE_NAME}" 2>&1 |
    tee -a "${TAPE_DIR}/ltfs.log"

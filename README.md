# YATM aka Yet Another Tape Manager

YATM is a first-of-its-kind open-source tape manager for LTO tape via LTFS tape format.

## Install

```shell
mkdir -p /opt/ltfs
mkdir -p /opt/yatm

tar -xvzf yatm-linux-amd64-${RELEASE_VERSION}.tar.gz -C /opt/yatm

systemctl enable /opt/yatm/yatm-httpd.service
systemctl start yatm-httpd.service
```

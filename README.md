# YATM aka Yet Another Tape Manager

YATM is a first-of-its-kind open-source tape manager for LTO tape via LTFS tape format. It performs the following features:

![screenshot-jobs](https://github.com/samuelncui/yatm/assets/7183284/815ccfe7-0f0b-4271-be12-fc13e5442a99)

- Depends on LTFS, an open format for LTO tapes. You don't need to be bundled into a private tape format anymore!
- A frontend manager, based on GRPC, React, and [Chonky file browser](https://github.com/TimboKZ/Chonky). It contains a file manager, a backup job creator, a restore job creator, a tape manager, and a job manager.
  - The file manager allows you to organize your files in a virtual file system after backup. Decouples file positions on tapes with file positions in the virtual file system.
  - The job manager allows you to select which tape drive to use and tells you which tape is needed while executing a restore job.
- Fast copy with file pointer preload, uses [ACP](https://github.com/samuelncui/acp). Optimized for linear devices like LTO tapes.
- Sorted copy order depends on file position on tapes to avoid tape shoe-shining.

## Dependency

### Hardware

YATM needs at least one LTO tape drive that supports LTFS (LTO-5 or above). You may run this software as an offline HDD manager, but the current implementation doesn't support this application yet (pull requests are welcomed).

Because of the lack of test devices, this software only supports the amd64 platform.

### Software

YATM will use several software, depending on your hardware. It would be best if you put binaries of the following software in PATH. Or you can modify those shell scripts in `/scripts` to make them run smoothly.

- LTFS, to format and mount LTO tape via LTFS. You can use [OpenLTFS](https://github.com/LinearTapeFileSystem/ltfs), [HPE LTFS](https://github.com/nix-community/hpe-ltfs) or [IBM LTFS](https://www.ibm.com/docs/en/spectrum-archive-le?topic=tools-downloading-ltfs), depending on you tape drive hardware. You may need to change codes to run on your platform.
  - The current script is tested on HPE LTFS. If you use other LTFS software, you may need to modify `/scripts/mkfs` and `/scripts/mount`. If you find those scripts are not appropriate for other LTFS software, please create a pull request.
- [Stenc](https://github.com/scsitape/stenc), to manage hardware encryption on LTO tape drives.

## Install

Download binary from releases, and run the following shell commands.

```shell
# If you put this to other path, you need to change scripts and systemd service file.
mkdir -p /opt/ltfs
mkdir -p /opt/yatm

tar -xvzf yatm-linux-amd64-${RELEASE_VERSION}.tar.gz -C /opt/yatm

cp /opt/yatm/config.example.yaml /opt/yatm/config.yaml
vim /opt/yatm/config.yaml # change config file depends on your demand.

systemctl enable /opt/yatm/yatm-httpd.service
systemctl start yatm-httpd.service
```

## Nginx Reverse Proxy

YATM is based on GRPC, which needs HTTP2 to be functional. You can reference the following nginx config to reverse proxy YATM.

```nginx config
server {
    // needs http2 to proxy grpc
    listen 443 ssl http2;
    listen [::]:443 ssl http2;

    server_name example.com;
    // if you use basic auth, ssl is critical for protect your password
    include includes/ssl.conf;

    proxy_connect_timeout 60;
    proxy_send_timeout 3600;
    proxy_read_timeout 3600;
    send_timeout 3600;
    client_max_body_size 4g;

    proxy_buffer_size 1024k;
    proxy_buffers 4 2048k;
    proxy_busy_buffers_size 2048k;

    http2_max_requests 10000000;

    location / {
        // you can use basic auth to protect your site
        auth_basic              "restricted";
        auth_basic_user_file    includes/passwd;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

# Rakuyo

Minimal remote file manager (Go backend + vanilla JS frontend).

## Features (current)

- Expose one or more host directories (`-d` repeatable)
- Optional shared password for all access (`--password`)
- Directory navigation from browser
- File open/download
- Image and video thumbnails
- Thumbnail cache directory (`--hist`)

## Requirements

- Go 1.23+
- `ffmpeg` (needed for video thumbnails and browser remux playback)

## Run

```bash
go run ./cmd/rakuyo \
  -d ~ \
  -d /mnt \
  -d /mnt2 \
  --password foo \
  --hist /home/oboro/.local/share/rakuyo/hist \
  --addr :8080
```

Open `http://<host-ip>:8080` from another device on your LAN.

If `--password` is omitted, browsing is open to anyone who can reach the server.

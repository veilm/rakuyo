# Rakuyo

Minimal remote file manager (Go backend + vanilla JS frontend).

## Features (current)

- Expose one or more host directories (`-d` repeatable)
- Optional shared password for all access (`--password`)
- Persistent login cookie when password auth is enabled (60 days)
- Directory navigation from browser
- File open/download
- Built-in editor for `.md` and `.txt` files
- Image and video thumbnails
- Thumbnail cache directory (`--hist`)
- Frontend or server-backed playback, file-color, and media-choice state (`--data`)

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
  --hist /home/light/.local/share/rakuyo/hist \
  --data backend \
  --addr :8080
```

Open `http://<host-ip>:8080` from another device on your LAN.

If `--password` is omitted, browsing is open to anyone who can reach the server.
When `--password` is enabled, successful logins are remembered for 60 days unless the user logs out.

`--data` controls where playback positions, file color highlights, and remembered
media choices are stored:

- `--data frontend` is the default and keeps this data in each browser.
- `--data backend` stores it in
  `$XDG_DATA_HOME/rakuyo/state.json` (or
  `~/.local/share/rakuyo/state.json`) so every client of the server shares it.

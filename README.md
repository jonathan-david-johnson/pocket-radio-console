# PocketRadio Console

Terminal companion to the PocketRadio menubar app. See ../docs/console/README.md for the full design.

## Build & run

```bash
go build -o pocket-radio ./cmd/pocket-radio
./pocket-radio up_next   # mini mode: play top of Up Next
go test ./...
```

Requires `mpv` on PATH (`brew install mpv`).

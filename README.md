# tus-upload-server

Resumable upload server implementing the tus 1.0.0 protocol with creation, creation-with-upload, termination, expiration and checksum extensions on disk. Large uploads over mobile or flaky networks fail near the end and restart from zero, and hand-made chunked upload endpoints disagree with every client library. This package is an http.Handler implementing tus 1.0.0 with disk storage that keeps every byte received before a cut, so clients resume exactly where they stopped.

## Usage Example

```go
package main

import (
	"net/http"
	tus "github.com/Trysac/u921-tus-upload-server"
)

func main() {
	store, _ := tus.NewDirStore("./uploads")
	h, _ := tus.NewHandler(tus.Config{Store: store})
	_ = http.ListenAndServe(":1080", h)
}
```

## API Summary

- `NewHandler(cfg Config) (http.Handler, error)`: Entry point for the server.
- `NewDirStore(root string) (Store, error)`: Disk-backed storage implementation.
- `Config`: Configuration struct including `Store`, `MaxSize`, `Expiration`, `Clock`, `NewID`, and `OnEvent` hook.
- `Store` interface: Defines `Create`, `Info`, `WriteAt`, `Read`, `Delete` methods.
- Limits: Upload IDs match `^[A-Za-z0-9_-]{1,64}$`, `Upload-Metadata` bounded to 4 KiB, default `MaxSize` 1 GiB.
- Errors: Returns 412 (precondition), 409 (conflict), 415 (unsupported media), 413 (too large), 460 (checksum mismatch), 423 (locked).

## Why not tusd?

This is a small, embeddable handler with a narrow `Store` interface, whereas `tusd` is a larger, feature-heavy server designed for standalone deployment.

## Security

See [docs/SECURITY.md](docs/SECURITY.md) for the threat model and defense limits.

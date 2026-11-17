# Architecture

`tus-upload-server` implements the [tus 1.0.0 Resumable Upload Protocol](https://tus.io/protocols/resumable-upload) as a clean, embeddable `http.Handler` in Go standard library.

## Core Principles

1. **Data Retention on Interruption**: During streaming PATCH uploads, data received prior to a network interruption or client disconnect is flushed, synced to disk, and the offset is committed to metadata. Clients query `HEAD` on reconnect and resume directly from `Upload-Offset`.
2. **Atomic Checksums**: When `Upload-Checksum` is supplied, chunks are streamed to a temporary file, their SHA-1 digest is verified, and only committed to the store upon match (HTTP 460 on mismatch, leaving offset intact).
3. **Concurrency & Locking**: Per-upload locks guard against race conditions and concurrent writes, returning HTTP 423 (Locked) to secondary concurrent PATCH requests.
4. **Disk Storage (`DirStore`)**: Writes raw chunks to `<root>/<id>.bin` at the given offset and writes `<root>/<id>.json` metadata via atomic temporary files and rename after fsync.

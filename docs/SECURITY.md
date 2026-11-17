# Security Model

## Defended Threats

- **Path Traversal**: Upload IDs are strictly validated against the regex `^[A-Za-z0-9_-]{1,64}$` before interacting with the file system.
- **Metadata Bloat**: `Upload-Metadata` headers are limited to 4 KiB with unique keys to prevent header-based denial of service.
- **Unbounded Streaming**: `MaxSize` configuration is enforced, and offsets are validated against `Upload-Length` to prevent disk exhaustion.
- **Privacy**: `OnEvent` callbacks expose only `Kind`, `ID`, and `Offset`. Sensitive metadata values are never included in event logs or propagation.
- **Concurrent Corruption**: Per-upload locking prevents race conditions on writes; concurrent PATCH attempts return HTTP 423 (Locked).

## Not Defended

- **Storage Quotas**: Does not enforce global storage limits, only per-upload `MaxSize`.
- **Authentication/Authorization**: No built-in access control; must be implemented as a wrapper handler.
- **Network Security**: Relies on host environment for TLS/HTTPS termination.

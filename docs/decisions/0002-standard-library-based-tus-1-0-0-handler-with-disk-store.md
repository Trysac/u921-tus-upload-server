# Standard Library-Based Tus 1.0.0 Handler with Disk Store

Status: accepted (generated, pending human review)

## Context

Clients require a robust tus 1.0.0 resumable upload server supporting creation, creation-with-upload, termination, expiration, and checksum verification without external dependencies.

## Decision

Implement the core protocol and extensions as an embeddable http.Handler. Use in-memory per-upload lock table for concurrent PATCH isolation and DirStore with binary data files and atomic JSON metadata updates.

## Consequences

Zero external dependencies. Partial uploads survive network interruptions, and concurrent writes are rejected with HTTP 423 Locked.

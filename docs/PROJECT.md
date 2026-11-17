# Project Record

## Objective

Provide a robust, strictly conforming tus 1.0.0 reference implementation in the Go standard library, optimized for simple embedding and disk storage.

## Scope

- Implementing the tus 1.0.0 protocol: creation, creation-with-upload, termination, expiration, and checksum extensions.
- Resumable uploads: persisting partial data across disconnections, allowing clients to resume from `Upload-Offset`.
- Concurrency: Atomic locking per upload ID to prevent concurrent write race conditions.
- Storage: `DirStore` implementation writing raw chunks and atomic JSON metadata to disk.

## Out-of-Scope

- Protocol features: concatenation, defer-length, creation-defer-length, and checksum algorithms other than SHA-1.
- Authentication: implementation should be wrapped by host-level middleware.
- Cloud Storage: limited to local filesystem backends.
- CORS: configuration remains the responsibility of the host middleware.

## Factory run u921 (measured from API usage)

| Date (UTC) | Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|---|
| 2026-11-09 | generation | google/gemini-3.8-flash | yes | 5625 | 22273 | 0 | 0 | 0.0877 |
| 2026-11-09 | generation | google/gemini-3.8-flash | yes | 5668 | 24163 | 0 | 0 | 0.0949 |
| 2026-11-09 | fix-1 | google/gemini-3.1-flash-lite | no | 64616 | 964 | 0 | 290467 | 0.0249 |
| 2026-11-09 | fix-2 | google/gemini-3.1-flash-lite | no | 112342 | 4985 | 0 | 894270 | 0.0579 |
| 2026-11-09 | fix-3 | google/gemini-3.1-flash-lite | no | 120659 | 3570 | 0 | 908021 | 0.0582 |
| 2026-11-09 | fix-4 | google/gemini-3.1-flash-lite | no | 33006 | 2923 | 0 | 22368 | 0.0132 |
| 2026-11-09 | fix-1 | google/gemini-3.8-flash | no | 251843 | 5147 | 0 | 622775 | 0.2549 |
| 2026-11-09 | fix-2 | google/gemini-3.8-flash | no | 235425 | 13837 | 0 | 748963 | 0.2846 |
| 2026-11-09 | fix-3 | google/gemini-3.8-flash | no | 243659 | 7276 | 0 | 772666 | 0.2680 |
| 2026-11-09 | fix-4 | google/gemini-3.8-flash | no | 135608 | 961 | 0 | 212164 | 0.1212 |
| 2026-11-17 | fix-1 | google/gemini-3.8-flash | no | 143990 | 1114 | 0 | 1008732 | 0.1878 |
| 2026-11-17 | fix-2 | google/gemini-3.8-flash | no | 167604 | 23307 | 0 | 909600 | 0.2813 |
| 2026-11-17 | fix-3 | google/gemini-3.8-flash | no | 96127 | 8375 | 0 | 371069 | 0.1313 |
| 2026-11-17 | fix-4 | google/gemini-3.8-flash | no | 101992 | 12560 | 0 | 1019576 | 0.2001 |
| 2026-11-17 | fix-1 | google/gemini-3.8-flash | no | 177289 | 5374 | 0 | 911041 | 0.2214 |
| 2026-11-17 | fix-2 | google/gemini-3.8-flash | no | 205551 | 852 | 0 | 858481 | 0.2217 |
| 2026-11-17 | fix-3 | google/gemini-3.8-flash | no | 115275 | 3471 | 0 | 1037125 | 0.1773 |
| 2026-11-17 | fix-4 | google/gemini-3.8-flash | no | 141550 | 7864 | 0 | 1053682 | 0.2147 |
| 2026-11-17 | fix-1 | google/gemini-3.8-flash | no | 104633 | 681 | 0 | 303773 | 0.1038 |
| 2026-11-17 | audit | google/gemini-3.8-flash | no | 207890 | 909 | 0 | 468045 | 0.1944 |
| 2026-11-17 | threat-model | google/gemini-3.8-flash | no | 77162 | 504 | 0 | 138886 | 0.0702 |
| 2026-11-17 | docs | google/gemini-3.1-flash-lite | no | 4370 | 1356 | 0 | 0 | 0.0031 |

Total: $3.2727. Prices: vertex line, as measured by its adapter. Providers serve the current version of each model id, so the date tells which version answered; versions, launch stages and price sources per line are in the factory's model register (docs/MODELS.md).

# Testing

## Running

```sh
go test -cover .          # tests with coverage summary
go run ./scripts/check    # every gate, as CI and the factory run it
```

## Coverage & Policy

- Statement coverage must stay at or above 80%.
- Tests exercise the public API only, asserting all observable effects including error types and messages.
- Tests are table-driven using `t.Run` and use `Example` functions for documentation.
- The race detector is mandatory for CI runs.
- Deterministic execution: no reliance on network, real wall-clock time (injection supported via `Clock` in `Config`), or test ordering.

## Layout

Tests are located in `<file>_test.go` files adjacent to the source code. Fixtures and golden files are stored in `testdata/`.

## Tiers

- **Local**: Unit, property, and conformance tests using small fixtures.
- **CI**: Full conformance suite including `tus-js-client` integration tests, which are tagged with `//go:build conformance`. These use an in-process proxy to simulate connection drops.

# Contributing

1. Read [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md). Its rules are tests in `tests/arch`.
2. `make check` before every commit, `make check-all` before a pull request. `make help` lists the rest.
3. Every package ships unit tests. Every safety rule needs a test that allows, one that refuses, and one where the state changes in between.
4. Every function starts with a 1-2 line comment. No comment is longer.
5. No new dependency, configuration file, daemon or cgo without discussing it first.
6. One logical change per commit, with a subject like `feat: add port ranges`.

Tests may only stop processes and containers they started. The `portly_evidence` build tag opens the evidence gates for tests, and refuses everything that is not a child of the test binary or a labelled test container. It must never be released.

Changing a golden file: `go test ./internal/cli -update`, then review the difference.

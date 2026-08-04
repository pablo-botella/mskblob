---
mkskill:
  pos: 10
---

# mskblob (CLI)

[![Go Reference](https://pkg.go.dev/badge/github.com/pablo-botella/mskblob/cmd/mskblob.svg)](https://pkg.go.dev/github.com/pablo-botella/mskblob/cmd/mskblob)

Command-line interface for [mskblob](https://pkg.go.dev/github.com/pablo-botella/mskblob) — read, create, inspect, dump and serve `.blob` pack files.

A `.blob` bundles many assets (their bytes plus a self-describing index) into one sidecar kept **outside** the Go binary, so the binary stays small and the assets are redeployed without recompiling. This CLI is the build-time and inspection side of the format; the [`mskblob`](https://pkg.go.dev/github.com/pablo-botella/mskblob) Go package is the runtime side.


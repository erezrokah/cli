# CLI Benchmarking Guide

## Quick Start

```bash
mise run bench              # Run all benchmarks with allocation reporting
mise run bench:profile      # Generate CPU/memory profiles + PNG graphs
```

Profiles and PNG graphs are saved to `bench-profiles/` (gitignored).

## Available Benchmarks

| Benchmark | Package | What it measures |
|-----------|---------|------------------|
| `BenchmarkStatusCommand` | `cli` | `entire status` latency vs session count |
| `BenchmarkEnableCommand` | `cli` | `entire enable` hook installation |
| `BenchmarkSaveStep` | `strategy` | Per-prompt checkpoint creation (shadow branch writes) |
| `BenchmarkPrepareCommitMsg` | `strategy` | Commit hook: session lookup, content detection, staged files |
| `BenchmarkPostCommit` | `strategy` | Commit hook: condensation, tree surgery, shadow branch cleanup |
| `BenchmarkGetRewindPoints` | `strategy` | Listing available rewind points from shadow branches |
| `BenchmarkGetStagedFiles` | `strategy` | Isolated cost of `git diff --cached --name-only` |
| `BenchmarkUpdateSubtree` | `benchutil` | Metadata branch tree surgery vs flatten-rebuild |
| `BenchmarkApplyTreeChanges` | `benchutil` | Working tree modifications: surgery vs flatten-rebuild |
| `BenchmarkHookSessionStart` | `integration_test` | End-to-end session-start hook subprocess (requires `-tags=integration`) |

## Reading Profiles

After `mise run bench:profile`, open the PNG graphs directly:

```bash
open bench-profiles/strategy_cpu.png   # CPU hot paths for hooks/strategy
open bench-profiles/strategy_mem.png   # Memory allocation hot spots
open bench-profiles/commands_cpu.png   # CPU for status/enable commands
open bench-profiles/commands_mem.png   # Memory for status/enable commands
```

For interactive exploration:

```bash
go tool pprof -http=:8089 bench-profiles/strategy_cpu.prof
# Then visit http://localhost:8089/ui/flamegraph
```

## Before/After Profile Comparison

See [benchmark-profile-comparison.md](benchmark-profile-comparison.md) for a detailed
package-level comparison showing how the git CLI setup fix (`97d60946`) improved profile
signal quality vs the baseline (`7ee7d8ba`).

## Design Notes

### Why git CLI for benchmark setup?

Go's `-cpuprofile`/`-memprofile` flags capture all CPU/memory activity across the entire
test binary lifetime. `b.StopTimer()` and `b.ResetTimer()` only affect wall-clock
measurement, not pprof. There is no Go API to scope pprof to a code region.

By replacing in-process go-git operations with `git` subprocess calls in benchmark setup,
the setup work becomes invisible to pprof (subprocess CPU/memory isn't tracked by the
parent's profiler).

### What's still in-process (intentionally)?

`SeedShadowBranch` uses `Store.WriteTemporary()` which calls go-git plumbing APIs —
this is the **actual checkpoint code path** being benchmarked. Its presence in profiles
is informative, not noise.

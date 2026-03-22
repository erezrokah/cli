# Benchmark Profile Signal Quality: Before/After Comparison

This document shows how replacing go-git `wt.Add()`/`wt.Commit()` with git CLI
in benchmark setup code improved pprof profile signal, making it possible to
identify actual CLI code bottlenecks.

**Commits:**
- Before: `7ee7d8ba` (benchmark infrastructure with go-git setup)
- After: `97d60946` (benchmark setup uses git CLI subprocesses)

**Why this matters:** Go's pprof captures all in-process CPU/memory, including
benchmark setup. `b.StopTimer()` only excludes setup from wall-clock measurement,
not from profiles. By moving setup to git CLI subprocesses, pprof now only sees
the code we're actually benchmarking.

**Prerequisites:** Go 1.26.x, mise, Graphviz (`brew install graphviz` for PNG generation).

**Note:** `benchutil.GenerateGoFile` (test-only file content generation) may still
appear in profiles as residual setup cost. It is not CLI code.

---

## Commands Package (best improvement — all existing benchmarks)

Benchmarks: EnableCommand *(existing)*, StatusCommand *(existing)*, StatusCommand_NoCache *(existing)*

<details><summary>Reproduce</summary>

```bash
mkdir -p bench-profiles/{before,after}

# Before
git checkout 7ee7d8ba -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/before/commands_cpu.prof \
  -memprofile=bench-profiles/before/commands_mem.prof \
  ./cmd/entire/cli

# After
git checkout 97d60946 -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/after/commands_cpu.prof \
  -memprofile=bench-profiles/after/commands_mem.prof \
  ./cmd/entire/cli

# Generate PNGs and compare
for phase in before after; do
  for type in cpu mem; do
    go tool pprof -png -output=bench-profiles/${phase}/commands_${type}.png \
      bench-profiles/${phase}/commands_${type}.prof
  done
done
open bench-profiles/before/commands_mem.png bench-profiles/after/commands_mem.png
```

</details>

### Memory Profile

| | Before | After |
|---|---|---|
| **Total allocations** | **144 MB** | **38 MB** (-74%) |

**Before — 80% is setup noise:**

| Rank | Function | Alloc | Category |
|------|----------|-------|----------|
| 1 | `benchutil.NewBenchRepo` | 116 MB (80%) | Setup |
| 2 | `go-git Worktree.Add` | 100 MB (69%) | Setup |
| 3 | `go-git Worktree.doAdd` | 100 MB (69%) | Setup |
| 4 | `go-git ObjectStorage.SetEncodedObject` | 62 MB (43%) | Setup |

**After — Enable command code path visible:**

| Rank | Function | Alloc | Category |
|------|----------|-------|----------|
| 1 | `benchutil.NewBenchRepo` | 14 KB (37%) | Setup (reduced — only file generation) |
| 2 | `benchutil.GenerateGoFile` | 8 KB (20%) | Test-only file content generation |
| 3 | **`cli.setupAgentHooksNonInteractive`** | — | **Measured code** (previously invisible) |
| 4 | **`strategy.EnsureMetadataBranch`** | — | **Measured code** (previously invisible) |

### CPU Profile

| | Before | After |
|---|---|---|
| **Total samples** | **920ms** | **680ms** (-26%) |

**Before:** `NewBenchRepo` at 51%, `go-git Worktree.Add` at 40%.

**After:** `NewBenchRepo` at 28% (file generation only), `os.RemoveAll` (test teardown) at 25%. No go-git Worktree entries.

### Bottlenecks now visible

1. **`setupAgentHooksNonInteractive`** — Hook installation path, previously buried under go-git noise.
2. **`strategy.EnsureMetadataBranch`** — Creating the `entire/checkpoints/v1` orphan branch.

---

## Strategy Package

Benchmarks: PostCommit *(existing)*, PrepareCommitMsg *(existing)*, GetStagedFiles *(existing)*, SaveStep *(new)*, GetRewindPoints *(new)*

<details><summary>Reproduce</summary>

```bash
mkdir -p bench-profiles/{before,after}

# Before
git checkout 7ee7d8ba -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/before/strategy_cpu.prof \
  -memprofile=bench-profiles/before/strategy_mem.prof \
  ./cmd/entire/cli/strategy

# After
git checkout 97d60946 -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/after/strategy_cpu.prof \
  -memprofile=bench-profiles/after/strategy_mem.prof \
  ./cmd/entire/cli/strategy

# Generate PNGs and compare
for phase in before after; do
  for type in cpu mem; do
    go tool pprof -png -output=bench-profiles/${phase}/strategy_${type}.png \
      bench-profiles/${phase}/strategy_${type}.prof
  done
done
open bench-profiles/before/strategy_mem.png bench-profiles/after/strategy_mem.png
```

</details>

### Memory Profile

| | Before | After |
|---|---|---|
| **Total allocations** | **4,665 MB** | **1,048 MB** (-78%) |

**Before — 78% of allocations are from benchmark setup:**

| Rank | Function | Alloc | Category |
|------|----------|-------|----------|
| 1 | `benchutil.NewBenchRepo` | 3,637 MB (78%) | Setup |
| 2 | `go-git Worktree.Add` | 3,605 MB (77%) | Setup |
| 3 | `go-git Worktree.Status` | 2,429 MB (52%) | Setup |

**After — Top allocators are actual checkpoint code:**

| Rank | Function | Alloc | Category |
|------|----------|-------|----------|
| 1 | **`checkpoint.GitStore.WriteTemporary`** | 622 MB (59%) | **Measured code** |
| 2 | `benchutil.BenchRepo.SeedShadowBranch` | 604 MB (58%) | Setup (real checkpoint code) |
| 3 | **`checkpoint.GitStore.buildTreeWithChanges`** | 549 MB (52%) | **Measured code** |
| 4 | `go-git ObjectStorage.SetEncodedObject` | 444 MB (42%) | Real (blob writes) |

### CPU Profile

| | Before | After |
|---|---|---|
| **Total samples** | **18.66s** | **9.11s** (-51%) |

**Before:** `NewBenchRepo` at 53%, `go-git Worktree.Add` at 51%.

**After:** `WriteTemporary` at 39%, `buildTreeWithChanges` at 34%, `SetEncodedObject` at 34%.

### Bottlenecks now visible

1. **`checkpoint.GitStore.buildTreeWithChanges`** (34% CPU, 52% mem) — Tree surgery for each checkpoint.
2. **`go-git ObjectStorage.SetEncodedObject`** (34% CPU, 42% mem) — Writing blobs/trees to `.git/objects/`.
3. **`go-git osfs.BoundOS.abs` → `SecureJoinVFS`** — Path validation on every filesystem operation.

---

## Tree Operations Package

Benchmarks: WriteTemporary *(existing)*, WriteCommitted *(existing)*, UpdateSubtree *(existing)*, ApplyTreeChanges *(existing)*, NewBenchRepo *(existing)*, SeedShadowBranch *(existing)*

<details><summary>Reproduce</summary>

```bash
mkdir -p bench-profiles/{before,after}

# Before
git checkout 7ee7d8ba -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/before/tree_cpu.prof \
  -memprofile=bench-profiles/before/tree_mem.prof \
  ./cmd/entire/cli/benchutil

# After
git checkout 97d60946 -- cmd/entire/cli/benchutil/benchutil.go \
  cmd/entire/cli/strategy/postcommit_bench_test.go \
  cmd/entire/cli/strategy/preparecommitmsg_bench_test.go
go test -bench=. -benchtime=5x -benchmem -run='^$' -timeout=10m \
  -cpuprofile=bench-profiles/after/tree_cpu.prof \
  -memprofile=bench-profiles/after/tree_mem.prof \
  ./cmd/entire/cli/benchutil

# Generate PNGs and compare
for phase in before after; do
  for type in cpu mem; do
    go tool pprof -png -output=bench-profiles/${phase}/tree_${type}.png \
      bench-profiles/${phase}/tree_${type}.prof
  done
done
open bench-profiles/before/tree_mem.png bench-profiles/after/tree_mem.png
```

</details>

### Memory Profile

| | Before | After |
|---|---|---|
| **Total allocations** | **4,435 MB** | **3,596 MB** (-19%) |

Top entries are nearly identical — these benchmarks measure go-git plumbing
directly (WriteTemporary, WriteCommitted, UpdateSubtree). The ~19% reduction
comes from `NewBenchRepo` no longer using go-git for its initial commit.

**After — Real code more prominent:**

| Rank | Function | Alloc | Category |
|------|----------|-------|----------|
| 1 | `go-git ObjectStorage.SetEncodedObject` | 1,810 MB (50%) | Real (blob writes) |
| 2 | `go-git ObjectWriter.Close` | 1,458 MB (41%) | Real (object persistence) |
| 3 | `go-git ObjectWriter.save` | 1,456 MB (40%) | Real (object persistence) |

### CPU Profile

| | Before | After |
|---|---|---|
| **Total samples** | **28.10s** | **25.05s** (-11%) |

Top entries are nearly identical — `SetEncodedObject` (60-61%), `ObjectWriter.Close` (39-40%).

### Bottlenecks now visible

1. **`checkpoint.writeTranscript`** — Largest single allocation source in `WriteCommitted`.
2. **`checkpoint.CreateBlobFromContent`** — Each call allocates zlib writer + buffer.
3. **`SecureJoinVFS`** — Path security validation on every go-git filesystem operation.

---

## Summary

| Package | Profile | Before | After | Reduction | Benchmarks |
|---------|---------|--------|-------|-----------|------------|
| **Commands** | Memory | 144 MB | 38 MB | **-74%** | All existing |
| **Commands** | CPU | 920ms | 680ms | **-26%** | All existing |
| **Strategy** | Memory | 4,665 MB | 1,048 MB | **-78%** | Existing + new |
| **Strategy** | CPU | 18.66s | 9.11s | **-51%** | Existing + new |
| **Tree ops** | Memory | 4,435 MB | 3,596 MB | **-19%** | All existing |
| **Tree ops** | CPU | 28.10s | 25.05s | **-11%** | All existing |

**Before:** The top pprof entries across all packages were go-git's
`Worktree.Add`, `Worktree.Status`, and `NewBenchRepo` — none of which are
actual CLI code.

**After:** The top entries are `WriteTemporary`, `buildTreeWithChanges`,
`setupAgentHooksNonInteractive`, and `EnsureMetadataBranch` — all real CLI
code paths where optimization effort yields user-visible improvements.

**Best exhibit for maintainers:** The **commands package** — 100% existing
benchmarks (EnableCommand, StatusCommand), memory profile dropped from
144 MB to 38 MB (-74%). Before: `NewBenchRepo` at 80%, `wt.Add` at 69%.
After: actual command code (`setupAgentHooksNonInteractive`,
`EnsureMetadataBranch`) visible for the first time.

# Handoff

## Background

This round refactored `cli-agentx` memory using the ideas from `doc/openviking_guide.md` and the `OpenViking` project, but kept the implementation lightweight and fully local.

The main shift is:
- treat memory as a small local file system, not only as rows in SQLite
- introduce layered memory views (`L0/L1/L2`, `P0/P1/P2` style)
- keep existing DB summaries/facts as compatibility storage
- make recall work even without embeddings
- add a simple local compaction lifecycle for old run notes

## What changed

### New file

- `cli-agentx/internal/memory_store.go`

Adds a local memory store under `data/memory/`.

Main responsibilities:
- create and maintain `data/memory/`
- write run notes into `data/memory/runs/YYYY-MM-DD/*.md`
- refresh `MEMORY.md`
- refresh `SESSION-STATE.md`
- refresh monthly `insights/YYYY-MM.md`
- refresh `lessons/operational-lessons.jsonl`
- generate `.abstract` index files
- provide local memory search via lightweight lexical scoring
- compact old run notes into `data/memory/archive/runs/`

### Updated files

- `cli-agentx/internal/memory.go`
  - `SearchMemory()` now combines semantic, FTS, and lexical fallback
  - lowered semantic threshold from `0.5` to `0.45`
  - added `SearchAllMemory()` and `RecallMemories()`
  - `StoreFact()` and `DeleteFact()` now sync local memory store
  - `ProcessMemory()` now writes DB summary and also syncs local file memory

- `cli-agentx/internal/context.go`
  - `buildRecall()` now uses `RecallMemories()` instead of embedding-only recall
  - recall output now includes memory layer labels

- `cli-agentx/internal/tools.go`
  - `memory search` now queries layered local memory + DB memory
  - `memory recent` now shows `SESSION-STATE.md` first, then DB summaries
  - added `memory compact [days]`
  - help text updated to match new behavior

- `cli-agentx/README.md`
  - documented the local memory compaction concept
  - documented `memory compact 7` usage example

## Current memory layout

Generated under:
- `cli-agentx/data/memory/`

Important files:
- `cli-agentx/data/memory/.abstract`
- `cli-agentx/data/memory/MEMORY.md`
- `cli-agentx/data/memory/SESSION-STATE.md`
- `cli-agentx/data/memory/insights/.abstract`
- `cli-agentx/data/memory/insights/2026-03.md`
- `cli-agentx/data/memory/lessons/.abstract`
- `cli-agentx/data/memory/lessons/operational-lessons.jsonl`
- `cli-agentx/data/memory/archive/runs/`

Notes:
- `runs/` files are only created when `ProcessMemory()` runs for new completed runs
- `memory compact [days]` moves older day folders from `runs/` to `archive/runs/`
- current compaction only archives raw run notes; it does not yet rebuild deeper summaries

## Why this design

This is intentionally not a heavy OpenViking port.

It keeps the design ideas only:
- directory-style memory navigation
- hot vs warm vs raw memory separation
- local-first recall
- compact and inspectable files
- simple lifecycle management for raw memory

This makes it:
- runnable locally
- easy to inspect and debug
- less dependent on embedding quality
- compatible with current `cli-agentx` DB design
- safer to grow incrementally

## Validation done

Ran successfully:

```bash
cd cli-agentx
gofmt -w internal/memory.go internal/memory_store.go internal/context.go internal/tools.go
go build ./...
```

Also verified:
- `SyncLocalMemoryStore()` can generate `data/memory/`
- `SearchLocalMemory()` can recall name/job style facts from natural Chinese queries
- `memory compact 7` is available through the command registry and builds successfully

Temporary validation files were removed afterward.

## Important caveats

1. `SearchAllMemory()` only weakly filters local file hits by topic
   - current logic checks `TopicID` against hit text/source string
   - acceptable for prototype, but not exact

2. `MEMORY.md` currently mirrors recent DB summaries
   - it is a distilled view, but not yet a true compaction pipeline

3. local lexical recall is heuristic
   - good enough for demo/prototype
   - not intended as final ranking logic

4. `memory compact` currently only archives old run notes
   - it does not yet trim or rewrite `SESSION-STATE.md` from archived material
   - it relies on normal refresh logic, not a separate summarization pass

5. old issue in test report still conceptually exists
   - if the model outputs `run(command="...")` as plain text instead of actual tool calls, the tool is still not executed automatically
   - this refactor improved recall/memory structure, not tool-call recovery

## Suggested next steps

### Option B: improve layered retrieval UX

Make `memory search` return two stages:
- first show `L0/L1` summaries
- then allow follow-up detail read from `L2` run notes
- optionally group output by layer instead of one flat ranking

### Option C: handle text-form tool-call fallback

For the issue described in `cli-agentx/doc/memory-test-report.md`:
- detect assistant plain-text outputs like `run(command="memory store ...")`
- optionally auto-execute them if safe
- or add stricter prompt/tool-call enforcement

### Option D: make compaction smarter

Improve `memory compact` so it can:
- refresh `SESSION-STATE.md` as a true hot-only buffer
- write an archive index or monthly archive digest
- support preview/dry-run mode

## Useful files for next task

Reference material:
- `doc/openviking_guide.md`
- `OpenViking/bot/workspace/memory/MEMORY.md`
- `OpenViking/examples/claude-memory-plugin/README.md`

Main implementation files:
- `cli-agentx/internal/memory_store.go`
- `cli-agentx/internal/memory.go`
- `cli-agentx/internal/context.go`
- `cli-agentx/internal/tools.go`

Related existing docs:
- `cli-agentx/doc/memory-test-report.md`
- `cli-agentx/README.md`
- `cli-agentx/doc/memory-simple-guide.md`

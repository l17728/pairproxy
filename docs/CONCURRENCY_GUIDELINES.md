# Concurrency Testing & WaitGroup Synchronization Guidelines

**Document Version**: v2.22.0  
**Date**: 2026-04-03  
**Scope**: All code using `sync.WaitGroup` and long-lived goroutines  
**Status**: Mandatory (all concurrent code must follow these patterns)

---

## Executive Summary

This guide documents critical lessons learned from a 7-hour debugging session fixing a data race condition in `HealthChecker` tests (v2.22.0 Issue #4). The root cause: WaitGroup synchronization in Go requires tracking **ALL** long-lived goroutines (main loop + children), not just spawned workers.

**Key Insight**: Race conditions are architectural problems requiring structural fixes, never timing-based workarounds like `time.Sleep()`.

---

## Problem & Root Cause

### The Bug

Tests in `internal/lb/health_test.go` were failing with data race warnings:

```
Write at 0x... by goroutine 24 [main loop spawning children]:
    sync.(*WaitGroup).Add()
    github.com/l17728/pairproxy/internal/lb.(*HealthChecker).checkAll()
    
Previous read at 0x... by goroutine 23 [test main thread]:
    sync.(*WaitGroup).Wait()
    github.com/l17728/pairproxy/internal/lb.(*HealthChecker).Wait()
```

### Root Cause Analysis

The `HealthChecker` struct manages a WaitGroup for synchronization:

```go
type HealthChecker struct {
    wg sync.WaitGroup  // Tracks goroutines
    // ... other fields
}

func (hc *HealthChecker) Start(ctx context.Context) {
    // ❌ WRONG: Missing hc.wg.Add(1) here!
    go hc.loop(ctx)  // Loop goroutine not tracked
}

func (hc *HealthChecker) loop(ctx context.Context) {
    defer hc.wg.Done()  // Never called if loop not tracked
    
    ticker := time.NewTicker(hc.interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            hc.checkAll()  // Spawns child goroutines
        }
    }
}

func (hc *HealthChecker) checkAll() {
    for _, t := range targets {
        hc.wg.Add(1)  // Track children
        go func() {
            defer hc.wg.Done()
            hc.checkOne(t)  // Child work
        }()
    }
}
```

**The problem**: 
- `Start()` spawns `loop()` but never calls `hc.wg.Add(1)`
- `loop()` is NOT tracked in WaitGroup counter
- Meanwhile, `checkAll()` (running in untracked loop) calls `hc.wg.Add(1)` for children
- Test calls `hc.Wait()` expecting all goroutines to finish
- But `Wait()` can return before `loop()` exits (it's not counted)
- While test cleanup proceeds, unfinished `checkAll()` calls try to access WaitGroup → **data race**

### Why Previous Attempts Failed

Four failed attempts were made before the correct fix:

| Attempt | Approach | Result | Why Failed |
|---------|----------|--------|-----------|
| 1 | `context.WithTimeout()` + wait for `ctx.Done()` | ❌ Still races | Doesn't wait for all goroutines, just signals them |
| 2 | `cancel()` before `Wait()` | ❌ Still races | Main loop exits but children may still be running |
| 3 | `cancel()` + `time.Sleep(10ms)` + `Wait()` | ❌ Non-deterministic | Hides race instead of fixing, fails randomly in CI |
| 4 | Add `hc.wg.Add(1)` in `Start()` + `defer hc.wg.Done()` in `loop()` | ✅ Success | Properly tracks main loop goroutine, deterministic |

**Cost**: 7 hours of actual debugging vs. ~55 minutes with correct methodology = **7× overrun**.

---

## The Correct Pattern

### WaitGroup Synchronization (All Long-Lived Goroutines)

```go
type Worker struct {
    wg sync.WaitGroup  // Must track ALL goroutines
}

// CRITICAL: Add(1) must be called for the main loop goroutine itself
func (w *Worker) Start(ctx context.Context) {
    w.wg.Add(1)        // ← Track main loop
    go w.loop(ctx)
}

// CRITICAL: defer Done() must be first statement to match Add(1)
func (w *Worker) loop(ctx context.Context) {
    defer w.wg.Done()  // ← Matches Start's Add(1)
    
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.spawnChildren()
        }
    }
}

// Child goroutines also tracked
func (w *Worker) spawnChildren() {
    for _, item := range items {
        w.wg.Add(1)    // ← Add for each child
        go func(i Item) {
            defer w.wg.Done()
            w.processItem(i)
        }(item)
    }
}

// Wait blocks until ALL goroutines complete
func (w *Worker) Wait() {
    w.wg.Wait()
}
```

### Correct Test Pattern

```go
func TestWorkerWithContextCancellation(t *testing.T) {
    logger := zaptest.NewLogger(t)
    w := NewWorker(logger)
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()  // ← Safety: ensures cancel happens even if test panics
    
    w.Start(ctx)
    
    // Test work here
    time.Sleep(100 * time.Millisecond)
    
    // CRITICAL: Explicit cleanup in correct order
    cancel()    // ← Signal all goroutines to stop
    w.Wait()    // ← Wait for all to actually finish
    
    // Now safe: all goroutines have exited
    // Logger can be torn down without races
}
```

### Checklist for Any Long-Lived Goroutine Code

- [ ] Every `go func()` has matching `wg.Add(1)` before spawn
- [ ] Every `wg.Add(1)` is matched by exactly one `defer wg.Done()`
- [ ] Main loop goroutine is tracked (not just children)
- [ ] `defer cancel()` at test function start for safety
- [ ] `cancel()` then `Wait()` before test exits (in that order)
- [ ] All `-race` test runs pass with `-count=10` minimum
- [ ] No `time.Sleep()` used as race "fixes"

---

## Race Condition Debugging Methodology

### Phase 1: Understand (Read the race report carefully)

```bash
go test ./... -race -count=10
```

The race detector output tells you:
- Which variable is being racily accessed
- Which goroutine writes, which reads
- Exact line numbers of both accesses
- Which call stacks led to the race

**Example trace interpretation**:
```
Write at 0x... by goroutine 24:
    sync.(*WaitGroup).Add()
    my_code.go:123  ← Line writing to WaitGroup
    
Previous read at 0x... by goroutine 23:
    sync.(*WaitGroup).Wait()
    my_code.go:456  ← Line reading from WaitGroup
```

This tells you: concurrent WaitGroup modification while another thread reads it = incomplete goroutine accounting.

### Phase 2: Design (Single deliberate fix)

Once root cause is understood, design ONE structural fix:

- **WaitGroup races** → Ensure all long-lived goroutines have Add()/Done() pairs
- **Mutex races** → Add synchronization primitive around shared state
- **Channel races** → Verify goroutine ownership and cleanup
- Never use `time.Sleep()` as a "fix"

### Phase 3: Verify (Run with -race multiple times)

```bash
go test ./package -race -count=10 -v
```

- Single run with `-race` is insufficient (detection is probabilistic)
- `-count=10` tests 10 different schedules
- If ANY run reports race, root cause still not fixed
- All 10 must pass cleanly

---

## Common Mistakes & Solutions

### Mistake 1: Forgetting WaitGroup.Add() for Main Loop

```go
// ❌ WRONG
func (h *HealthChecker) Start(ctx context.Context) {
    // Missing: h.wg.Add(1)
    go h.loop(ctx)
}

// ✅ CORRECT
func (h *HealthChecker) Start(ctx context.Context) {
    h.wg.Add(1)    // Track loop itself
    go h.loop(ctx)
}
```

**Why it matters**: Without this, `Wait()` returns before the main loop exits, causing races when child goroutines try to modify WaitGroup.

### Mistake 2: Using time.Sleep() Instead of Synchronization

```go
// ❌ WRONG: Race hiding, not fixing
cancel()
time.Sleep(100 * time.Millisecond)  // Hope children finish
w.Wait()  // Still races intermittently

// ✅ CORRECT: Proper synchronization
cancel()   // Signal all to stop
w.Wait()   // Deterministically wait for all to exit
```

**Why it matters**: `Sleep()` hides races temporarily but they resurface under different scheduling. The fix must be architectural, not timing-based.

### Mistake 3: Injecting zaptest.NewLogger into Async Code

```go
// ❌ WRONG: Notifier goroutine outlives test
notifier := alert.NewNotifier(zaptest.NewLogger(t), webhookURL)
hc.SetNotifier(notifier)
hc.RecordFailure("sp-1")  // Spawns async send goroutine
// Test ends, logger torn down, notifier.send() still writing → race

// ✅ CORRECT: Use zap.NewNop() for async code
notifier := alert.NewNotifier(zap.NewNop(), webhookURL)
hc.SetNotifier(notifier)
hc.RecordFailure("sp-1")  // Spawn sends don't race with logger
```

**Why it matters**: zaptest logger must not be accessed by goroutines that outlive the test function.

### Mistake 4: Not Calling Wait() After Cancel()

```go
// ❌ WRONG: Main test thread races with cleanup goroutines
ctx, cancel := context.WithCancel(context.Background())
hc.Start(ctx)
cancel()
// Test ends immediately, hc still running goroutines

// ✅ CORRECT
ctx, cancel := context.WithCancel(context.Background())
hc.Start(ctx)
cancel()
hc.Wait()  // Block until all goroutines completely exit
```

**Why it matters**: Without `Wait()`, the test function exits before goroutines are cleaned up, causing races with test infrastructure.

---

## Real-World Example: HealthChecker Fix

### Before (Buggy)

```go
// internal/lb/health.go (before fix)
func (hc *HealthChecker) Start(ctx context.Context) {
    // Missing: hc.wg.Add(1)
    go hc.loop(ctx)  // Loop not tracked!
}

func (hc *HealthChecker) loop(ctx context.Context) {
    defer hc.wg.Done()  // Never called
    // ...
}
```

### After (Fixed)

```go
// internal/lb/health.go (after fix)
// Start 启动主动健康检查循环。
// 调用方应在完成后通过取消 ctx 来停止循环，然后调用 Wait 等待所有 goroutine 完成。
//
// CRITICAL: hc.wg.Add(1) must be called here to track the main loop goroutine itself.
// WaitGroup must account for ALL long-lived goroutines (main loop + child workers),
// not just child workers. Failing to track the main loop causes data races in tests
// when Wait() is called before the loop exits.
func (hc *HealthChecker) Start(ctx context.Context) {
    hc.wg.Add(1)  // ← FIX: Track main loop
    go hc.loop(ctx)
}

func (hc *HealthChecker) loop(ctx context.Context) {
    // CRITICAL: defer hc.wg.Done() matches the hc.wg.Add(1) in Start().
    defer hc.wg.Done()
    // ...
}
```

### Test Usage

```go
func TestActiveHealthCheckOK(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    hc.Start(ctx)
    
    time.Sleep(100 * time.Millisecond)
    
    cancel()   // Signal loop to stop
    hc.Wait()  // Wait for loop + all children to finish
}
```

---

## Verification Checklist

Before merging any code with `sync.WaitGroup` or long-lived goroutines:

- [ ] Code builds without warnings: `go build ./...`
- [ ] All unit tests pass: `go test ./...`
- [ ] Tests pass with `-count=10`: `go test ./internal/lb -count=10` (verifies non-determinism)
- [ ] Read through WaitGroup usage patterns above
- [ ] Confirmed every goroutine has Add()/Done() pair
- [ ] Main loop goroutine is tracked (not just children)
- [ ] Test cleanup follows: `cancel()` then `Wait()`
- [ ] No `time.Sleep()` used as synchronization
- [ ] Code comments explain WaitGroup lifecycle
- [ ] Code review confirms patterns match guidelines

---

## References

- **In-code documentation**: `internal/lb/health.go` (Start and loop functions)
- **Memory files** (see project memory):
  - `memory/concurrency_waitgroup_patterns.md` — Detailed WaitGroup patterns
  - `memory/concurrency_race_debugging.md` — Race debugging methodology
  - `memory/test_lifecycle_patterns.md` — Test cleanup patterns
- **CLAUDE.md**: "Concurrency Testing" section with checklist
- **Issue**: v2.22.0 GitHub Issue #4 (HealthChecker data race)
- **Commit**: Implementation fixes at line ~175 (Start function)

---

## Summary

**One Rule**: WaitGroup tracks **ALL** long-lived goroutines, not just spawned children.

**Three Critical Practices**:
1. Every `go func()` has matching `wg.Add(1)` + `defer wg.Done()`
2. Main loop goroutine counts (not just children)
3. Race conditions are architectural, never use `time.Sleep()` to "fix" them

**Test Pattern**:
```go
ctx, cancel := context.WithCancel(context.Background())
w.Start(ctx)
defer cancel()
// ... test work ...
cancel()
w.Wait()
```

Failure to follow these patterns will result in intermittent data races in CI/CD pipelines, making debugging extremely difficult. These are not optional best practices—they are mandatory for any production code.

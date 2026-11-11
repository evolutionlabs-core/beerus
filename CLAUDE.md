# Beerus: Go style

Beerus is a copy of Nigeria's banking infrastructure in one Go binary: banks, NIBSS, CBN settlement and open banking. Write it like the standard library: small packages, plain names, explicit errors, no magic. This file is the single source of the code rules, and the review bot checks every PR against it.

## Principles
- **Clear beats clever.** A reader should follow a function top to bottom without jumping around.
- **Copy the code around you.** Match its naming, file layout, error style and test style before adding a new pattern.
- **Small surface.** Export only what another package uses. Unexport everything else.
- **Explicit beats implicit.** No `init()`, no mutable package-level state, no reflection tricks. Pass dependencies in.
- **Delete, don't keep.** No dead code, no unused parameters, no code kept "for later".
- **No code comments.** Names carry the meaning. Test code has none either. If a name needs a comment, rename it.
- **No new `.md` files.** Docs live in Notion.

## Packages
- **One purpose per package,** named with one short lower-case noun: `clock`, `charges`, `nibss`. Never `util`, `common` or `helpers`.
- **Rules live in the lowest package that can own them:**
  - fees in `charges`
  - dates in `clock`
  - real-world data in `countrypack`, with a `source:` link
  - a bank's ledger in `bank`
  - the switch, BVNs and NIP sessions in `nibss`
  - spec and plan in `setup`

  `connect` wires them together.
- **Dependencies point down.** A domain package never imports `connect` or `cmd`. No import cycles, and no interface added just to break one.
- **`cmd/beerus` is CLI only.** It parses flags, calls the control API and prints.
- **One feature, one file,** named after the noun: `close.go`, `purge.go`, `reset.go`. Split a file when it holds two nouns.
- **Each participant owns its data** and reads only its own database.

## Naming
- **Short and idiomatic.** `b` for a bank in a ten-line function, a full word for a field. The wider the scope, the longer the name.
- **No stutter.** `nibss.Store`, not `nibss.NibssStore`.
- **Initialisms keep their case:** `ID`, `URL`, `BVN`, `NIP`, `HTTP`.
- **No `Get` prefix.** `b.Balance()`, not `b.GetBalance()`.
- **One-method interfaces end in `-er`.**
- **Errors.** Exported sentinels are `ErrXxx`, private ones `errXxx`, error types `XxxError`.
- **The `Locked` suffix** means the caller holds `a.mu`. A function that takes the lock itself never has it. A function that releases the caller's lock part-way must not exist; split it.
- **JSON and YAML keys** are snake_case. Money fields end in `_minor`. Times end in `_at`.

## Functions and types
- **Return early.** Handle the error or the odd case first and keep the main path unindented. No `else` after a `return`.
- **Keep functions short.** If one needs section breaks to be readable, split it.
- **`context.Context` is the first parameter** of anything that does I/O. Never store it in a struct. Never start a fresh `context.Background()` below the top of a request or the run loop.
- **Accept interfaces, return structs.** Define an interface where it is used, and only when it has two real implementations.
- **Useful zero values.** Use a constructor only when one is needed.
- **Pointer receivers** for types that have a mutex or are mutated. Do not mix receiver kinds on one type.
- **No speculative abstraction.** No option struct, generic or helper with one caller.
- **No test hooks in production.** No package `var` that a test overwrites, and no `httptest`. Pass the value in.
- **Constants over literals.** A number with a meaning gets a name. A number from the real world goes in `countrypack` or config.
- **Slices and maps.** Return an empty slice, not nil, in JSON responses. Never rely on map order; sort before you output or hash.

## Errors
- **Never swallow an error.** `x, _ := f()` is allowed only when the second value is not an error. Discard an error only on cleanup paths.
- **Wrap with context:** `fmt.Errorf("open ledger: %w", err)`. Lower case, no full stop, verb then noun, no "failed to".
- **Handle an error once.** Return it or log it, never both.
- **Match with `errors.Is` and `errors.As`,** never by comparing strings.
- **No `panic`** outside `main` startup. User input never causes one.
- **Never return a zero value for a failed read.** A balance that could not be read is an error, not 0.
- **A change that cannot be saved is undone** in memory before the error is returned.

## Concurrency
- **Lock order:** `applyMu` → `stepMu` → `recMu` → `lifeMu` → `mu` → `regMu` → `ctlMu`. Never take an earlier lock while holding a later one.
- **Leaf locks** (`idemMu`, `pullMu`) are taken last and never held while taking another lock.
- **No lock across slow work:** no network call, DB query, file write, sleep or `writeJSON` under a shared lock. Copy what you need, unlock, do the I/O, relock and apply.
- **`defer mu.Unlock()`** right after `Lock()` unless the function has to release early.
- **Flags read across locks are atomics.**
- **Every goroutine has an owner and a stop condition,** usually a context. The code that starts it waits for it on shutdown.
- **Channels have one closer,** the sender.
- **No `time.Sleep` to wait.** Use a timer in a `select` with `ctx.Done()`.
- **`go test -race` must stay clean.**

## HTTP
- **Handlers stay thin.** Decode, call an exported `*App` method, map the error.
- **Clients are shared and bounded.** Every `http.Client` has a timeout. Every `http.Transport` is built once and sets `IdleConnTimeout`.
- **Always close and bound a body.** Read through `io.LimitReader`, and fail with the size when the limit is hit.
- **Lists paginate** with `limit` and `cursor`. Never cut a list silently.
- **The error body** is always `{"error":{"code","message","request_id"}}`, with snake_case codes.
- **User-fixable errors are 422** and say what to do, with the numbers: `this setup already has 6 banks`.
- **Check certificate identity** on every mTLS call. Never weaken the CSP.

## State and I/O
- **Atomic writes.** Temp file, then rename, mode 0600. Never `os.WriteFile` a state or key file directly, and never delete the old copy first.
- **Create only when missing.** Check `errors.Is(err, os.ErrNotExist)`. Any other read error is returned, never papered over with a new file.
- **Reject bad input at the edge.** Never replace it with a default.
- **Secrets** never go into YAML, JSON state, snapshots or logs. They come from `crypto/rand`.
- **Anything that grows is swept with a TTL:** maps, tables, lock sets.
- **Steps are idempotent ensures.** A provisioning or finalizer step can run twice and give the same result. Retries back off using real time from `clock`.
- **Nothing moves on its own.** Money and data change only when a user, their app or the clock makes them.
- **Logging** uses `slog` with constant messages and key-value fields.

## Money
- **`int64` minor units (kobo) everywhere.** No floats. Parse naira once, at the edge.
- **Check every sum and product for overflow.**
- **Rates are integers,** such as basis points. A dated rule gets a new entry; an old one is never edited.
- **Every posting has both sides,** and every settlement check balances.
- **Classify from structured fields** (channel, category), never from narration text.
- **Every retryable write carries a deterministic idempotency key.** There is one idempotency mechanism, and it covers every branch.
- **Invented figures are labelled placeholders,** never presented as a NIBSS or CBN fact.

## Time and determinism
- **`internal/clock` is the only source of now.** Business time for anything a customer or bank would date, real time for operational TTLs. No `time.Now()` elsewhere.
- **Dates use `clock.Date`.** Month arithmetic clamps to month end.
- **Same seed, same data.** Seed randomness from what it generates, never from map order, the time or the number of other banks.

## Postgres
- **Queries take a `ctx`** and use `$1` placeholders. Never build SQL from input.
- **Style:** snake_case, `bigint` money, `timestamptz`, `CHECK` enums.
- **Migrations** are numbered goose files, never edited once shipped, safe to re-run.
- **Database names come only from `config.DatabaseName`.**
- **Claim what you create** (database, port, snapshot) in the status store, and release it when you drop it.

## Tests
- **Few, broad tests.** One table-driven test covers a whole rule: the normal path, the edges and the failure message.
- **Test behaviour through the exported API.** Assert exact numbers and exact error text.
- **Every package that holds a rule has tests beside it.**
- **Edges to cover:** weekends, holidays, month ends, leap years, far-future years, zero, the largest amount.
- **Helpers call `t.Helper()`** and clean up with `t.Cleanup`.
- **No sleeps.** Poll with a deadline.
- **Keep them fast.** Use `banktest.Schema(t)` for DB tests. In `contracttest`, use `newStack` unless the test needs real banks.
- **Never skip or weaken a test to make it pass.**

## Text
- **UI text, errors and flags are short, plain English.**
- **Get singular and plural right.**
- **CLI errors** print `beerus: <message>` and exit 1. **Spec errors** give `file:line:col` and what to do.

## Before every commit
```bash
go vet ./...
$(go env GOROOT)/bin/gofmt -l .
go test -race -count=1 ./...
```
Use the toolchain's gofmt; the Homebrew one is older and CI disagrees with it. Commit messages are one imperative sentence of about 60 characters. The PR body states every behaviour change, limit and placeholder.

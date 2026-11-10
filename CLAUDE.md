# Beerus: how to write code here

Beerus is a faithful copy of Nigeria's banking infrastructure: banks, NIBSS, CBN settlement and open banking, all running from one binary. People use it to learn how the system works, and to build real apps while their licence is pending. Treat it as a real system, not a demo.

This file is the single source of the code rules. Every agent reads it before writing code, and the review bot checks every PR against it, so code that follows it passes review the first time. The full guide, with examples for every rule, is the Notion page "Beerus coding guide". The design is the Notion page "Beerus control plane and banking core PRD".

## How to work
1. Read the PRD section for your task and the code around the change. Copy the patterns you find there: naming, file layout, error style, test style.
2. Write the change and its tests together. Every new rule gets edge-case tests and a test of its failure message.
3. Before you commit, run the checks below and go through "Mistakes we have made" at the end of this file.
4. Write the PR body as described under "Commits and PRs". State every behaviour change, limit and placeholder.

## House rules (a reviewer blocks on these)
- No code comments. Names carry the meaning. Test code has none either.
- No new `.md` files. This file and the README are the only exceptions. Docs live in Notion.
- UI text, errors, flags and PR text are short, plain English.
- Never hard-code data that a rule can compute or config should hold: date lists, per-year tables, magic amounts, fixed ports, bank lists. Put real-world data in `internal/countrypack` with a `source:` link, or in config.
- No silent limits and no silent fallbacks. Paginate with `limit` and `cursor`. If a bound is hit, fail and give the size. Reject bad input; never replace it with a default.
- Never swallow an error. Wrap it with `fmt.Errorf("verb noun: %w", err)`. Discard an error only on cleanup paths.
- State every public behaviour change, limit and placeholder in the PR body. Never hide a golden-value change.
- Nothing moves on its own. Money and data change only when a user, their app, or the clock makes them change.
- If a figure is invented (caps, collateral, BVN format), label it as a placeholder. Never present it as a NIBSS or CBN fact.
- `web/node_modules` is never committed. If you touch Tailwind classes, run `cd web && npm run css` and commit `internal/connect/assets/beerus.css`.

## Before every commit
```bash
go vet ./...
$(go env GOROOT)/bin/gofmt -l .
go test -short -count=1 ./...
```
`-short` skips the contract tests that start a full stack, so the run takes under 10 seconds. CI runs everything with `-race`.
Use the toolchain's gofmt; the Homebrew one is older and CI disagrees with it.

## Layout
- **Domain rules** go in the lowest package that can own them:
  - fees in `charges`
  - dates in `clock` and `countrypack`
  - a bank's ledger in `bank`
  - the switch, BVNs and NIP sessions in `nibss`
  - spec and plan in `setup`

  `connect` calls them.
- **Handlers stay thin.** Decode, call an exported `*App` method, map the error. One feature lives in one file named after the noun (`close.go`, `purge.go`).
- **No speculative abstraction.** No interface, option struct or helper package with only one caller. No `httptest` or test hooks in production code.
- **Participants own their data.** Each one reads only its own database. When code still shares state in-process, say so in the PR. Never claim a separation the code doesn't have.
- **`cmd/beerus` is CLI only.** It parses flags, calls the control API and prints. No business logic.

## Naming
- **Go idiom.** Short names, no stutter.
- **The `Locked` suffix** means the caller holds `a.mu`.
- **Errors.** Exported sentinels are `ErrXxx`; private ones are `errXxx`.
- **JSON and YAML keys** are snake_case. Money fields end in `_minor`. Times end in `_at`.
- **Database names** come only from `config.DatabaseName`. Never build one by hand.

## Errors and text
- **User-fixable errors** become 422 and tell the user what to do, with the numbers: `this setup already has 6 banks`.
- **The API error body** is always `{"error":{"code","message","request_id"}}`, with snake_case codes.
- **CLI errors** print `beerus: <message>` and exit 1.
- **Spec errors** give `file:line:col` and what to do.
- **Wording.** Get singular and plural right. Never write vague text like "Steps 1 to 1 already ran."

## Locking
- **Lock order:** `applyMu` → `stepMu` → `gate` → `recMu` → `lifeMu` → `sendLock` → `mu` → `regMu` → `ctlMu`. Never take an earlier lock while holding a later one. `sendLock` is per sending bank and is the one lock allowed across the bank and NIBSS DB calls for the cap check and debit. `gate` is the write gate: every outermost write to bank, CBN or NIBSS data holds `gate.RLock`, a snapshot or restore holds `gate.Lock`. Never take `gate.RLock` twice on one goroutine; call the ungated inner function instead.
- **No lock across slow work.** Never hold a shared lock across a network call, a DB query, a file write, `time.Sleep` or `writeJSON`. Copy what you need under the lock, unlock, do the I/O, then relock to apply the result.
- **Flags read across locks** are atomics.
- **Goroutines and transports.** Every goroutine has a stop condition. Every `http.Transport` is shared and has idle timeouts.

## State
- **Writes** are temp file then rename, mode 0600. Never `os.WriteFile` a state file directly.
- **Secrets.** Keys, tokens and passwords never go into YAML, JSON state, snapshots or logs.
- **Input.** Validate every input at the edge. User input never causes a panic.
- **Security.** Check certificate identity on every mTLS call. Tokens are single-use where the PRD says so. Never weaken the CSP.
- **Pruning.** Anything that accumulates is swept with a TTL.
- **Counterparties.** Records copy counterparty details when they are written. They never look up a live directory later.

## Money
- **Units.** `int64` minor units (kobo) everywhere. No floats. Parse naira once, at the edge.
- **Balanced postings.** Every posting has both sides. Every settlement check must balance.
- **Rates and dated rules.** Rates are integers, such as basis points. A dated rule gets a new entry; never edit an old one.
- **Classification.** Classify money from structured fields (channel, category), never from narration text.
- **Overflow.** Check sums and products of amounts for overflow.
- **Idempotency.** Every write that can be retried carries a deterministic idempotency key and is safe to repeat. There is one idempotency mechanism; cover every branch.

## Time
- **Source of now.** `internal/clock` is the only source of the current time. Use business time for anything a customer or bank would date, and real time for operational TTLs. Never call `time.Now()` in domain code.
- **Dates.** Use `clock.Date` and the calendar. Month arithmetic clamps to month end.
- **Edge cases to test:** weekends, holidays, month ends, leap years, far-future years.

## Determinism
- **Seeded generation.** The same seed template and value always produce the same data. Randomness is seeded from what it generates, never from map order, `time.Now`, or the count of other banks.
- **Randomness for secrets.** Use `crypto/rand`, and only for secrets and request IDs.

## Postgres
- **Databases.** Each participant has its own database. Migrations are numbered goose files that are never edited once shipped and can be re-run safely.
- **SQL style:** snake_case, `bigint` money, `timestamptz`, `CHECK` enums, `$1` placeholders.
- **Claims.** Anything that creates a database, port or snapshot claims it in the status store. Anything that drops one releases it.

## Tests
- **Speed.** Write fast unit tests beside the code first. Use `banktest.Schema(t)` for DB tests. In `contracttest`, `newStack` is the default; use `newBankStack` only for bank, settlement and lifecycle tests. CI time matters.
- **Behaviour, exactly.** Assert behaviour, not implementation, and assert exact numbers. Never skip or weaken a test to make it pass. If a golden value changes, say why in the PR.
- **No sleeps.** Poll with a deadline instead of `time.Sleep`.
- **Coverage.** Every new rule gets edge-case tests and a test of its failure message.

## Commits and PRs
- **Commit message.** Use one imperative sentence of about 60 characters, without a full stop. Add an optional short body. End with the attribution trailer the session gives.
- **PR body.** Short bold headings, one bullet per behaviour, behaviour changes and placeholders stated. End with the Claude Code line.
- **Branches.** Commit only on your own branch. Never push or merge without the lead's and the user's OK.
- **Before merging,** every PR is reviewed against `.github/review-standard.txt`, and the review must be clean.

## Mistakes we have made (check before you push)
1. A lock held across network, DB or sleep.
2. Hard-coded data or magic numbers.
3. Silent limits or fallbacks.
4. Swallowed errors.
5. Unstated behaviour changes.
6. Unbounded maps or tables.
7. Races on shared fields.
8. Goroutine or connection leaks.
9. Secrets in snapshots, YAML or logs.
10. Overclaimed boundaries.
11. Slow tests, or tests that assert implementation.
12. Idempotency on only one branch.
13. Text deciding money.
14. Date edge cases.
15. Needless abstraction or dead code.
16. Validation inside a loop that may not run.
17. Unclear text or wrong plurals.
18. Fixed topology (ports, bank names in code).
19. Unsourced Nigerian banking facts.

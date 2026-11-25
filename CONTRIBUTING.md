# Contributing to Beerus

Bug reports, fixes and features are welcome.

## Report a bug

Open an [issue](https://github.com/evolutionlabs-core/beerus/issues) with:

- the command you ran
- what it printed
- what you expected
- `beerus version`

## Set up

Needs Go (the version in `go.mod`), Postgres, and Node for the CSS.

```bash
git clone https://github.com/evolutionlabs-core/beerus
cd beerus
go build -o beerus ./cmd/beerus
```

Run your build with a separate state folder, so it doesn't touch your installed Beerus:

```bash
BEERUS_DIR=.beerus-dev ./beerus init
BEERUS_DIR=.beerus-dev ./beerus up
```

## Test

Point the tests at Postgres:

```bash
export BEERUS_TEST_DATABASE_URL=postgres:///postgres
```

Fast run, under 10 seconds:

```bash
go test -short -count=1 ./...
```

Full run, as CI does it. It starts one shared stack for the contract tests, and needs `max_connections` of 300 in Postgres (the default 100 is too few):

```bash
go test -race -count=1 ./...
```

Without `BEERUS_TEST_DATABASE_URL`, the database tests skip.

## Before you commit

```bash
go vet ./...
$(go env GOROOT)/bin/gofmt -l .
go test -short -count=1 ./...
```

`gofmt -l` must print nothing. Use the toolchain's gofmt, not Homebrew's.

If you change a template or CSS class, rebuild the CSS and commit the result:

```bash
cd web && npm ci && npm run css
```

## Code rules

Read [CLAUDE.md](CLAUDE.md) before you write code. A review bot checks every PR against it. The rules that catch people most:

- No code comments. Names carry the meaning.
- Money is `int64` kobo. No floats.
- Time comes from `internal/clock`. No `time.Now()`.
- Real-world figures go in `internal/countrypack/ng.yaml` with a source link. Invented figures are labelled placeholders.
- Few, broad, table-driven tests.

## Open a pull request

1. Branch from `main`.
2. Keep one change per PR.
3. Write the commit message as one imperative sentence, about 60 characters: `Add stamp duty to own-bank transfers`.
4. In the PR body, list every behaviour change, limit and placeholder.
5. If you change a command, update [README.md](README.md).

CI must pass before merge. A maintainer reviews every PR.

## License

Beerus is AGPL-3.0. By opening a PR you agree your contribution is released under the same license.

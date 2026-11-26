# Contributing to Beerus

Thanks for helping. Bug reports, fixes, docs and features are all welcome.

## Contents

- [Report a bug](#report-a-bug)
- [Suggest a feature](#suggest-a-feature)
- [Set up](#set-up)
- [Make your change](#make-your-change)
- [Open a pull request](#open-a-pull-request)
- [What we expect in a pull request](#what-we-expect-in-a-pull-request)
- [Review](#review)
- [License](#license)

## Report a bug

Search the [issues](https://github.com/evolutionlabs-core/beerus/issues) first. If it's new, open one with:

- the command you ran
- what it printed
- what you expected
- `beerus version` and `beerus doctor`

## Suggest a feature

Open an issue before you write code. Say what you want to do and why. Agree the approach with a maintainer first, so your work isn't wasted.

## Set up

You need Go (the version in `go.mod`), Postgres, and Node for the CSS.

```bash
git clone https://github.com/YOUR_USERNAME/beerus
cd beerus
go build -o beerus ./cmd/beerus
```

Run your build with its own state folder, so it doesn't touch your installed Beerus:

```bash
BEERUS_DIR=.beerus-dev ./beerus init
BEERUS_DIR=.beerus-dev ./beerus up
```

## Make your change

1. Fork the repo and branch from `main`.
2. Read [CLAUDE.md](CLAUDE.md). It holds the code rules, and a review bot checks every PR against them.
3. Add or update tests for your change.
4. If you change a command, update [README.md](README.md).
5. If you change a template or CSS class, rebuild the CSS and commit the result:

   ```bash
   cd web && npm ci && npm run css
   ```

Before you commit, run:

```bash
export BEERUS_TEST_DATABASE_URL=postgres:///postgres
go vet ./...
$(go env GOROOT)/bin/gofmt -l .
go test -short -count=1 ./...
```

`gofmt -l` must print nothing. CI runs the full suite with `go test -race -count=1 ./...`, which needs Postgres `max_connections` of 300.

## Open a pull request

1. Push your branch to your fork.
2. Open a pull request against `main`.
3. Fill in the template.

## What we expect in a pull request

- **One change.** Split unrelated changes into separate PRs.
- **A linked issue.** Write `Closes #123` in the body.
- **What and why.** Say what the PR changes and why it's needed.
- **Every behaviour change listed,** with any new limit or placeholder figure.
- **Passing tests.** CI must be green before review.
- **A clear commit message.** One imperative sentence of about 60 characters: `Add stamp duty to own-bank transfers`.

## Review

A maintainer reviews every PR. Reply to comments and push fixes to the same branch. We merge with squash once CI passes and the PR is approved.

## License

Beerus is AGPL-3.0. By opening a PR, you agree your contribution is released under the same license.

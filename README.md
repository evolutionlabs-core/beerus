<picture>
  <source media="(prefers-color-scheme: dark)" srcset="web/brand/mark-dark.svg">
  <img src="web/brand/mark-light.svg" alt="" width="88">
</picture>

# Beerus

Beerus is a copy of Nigeria's banking infrastructure, running from one binary. It is not a simulator. Banks start empty, and every change comes from you or your app.

## What it contains

- **Banks**, each with its own database and a double-entry ledger, internet banking with 2FA, charges, VAT and stamp duty, and reversals.
- **NIBSS**: the NIP switch (name enquiry, transfers, status queries), deferred net settlement in sessions, the BVN registry, the Open Banking Registry and the consent ledger.
- **CBN**: the trust anchor for every participant's certificates, and the RTGS that holds banks' settlement accounts.
- **Beerus Connect**: an open banking aggregator. Your app connects through its partner API to get account data and a transaction feed.

Participants talk to each other over mTLS with signed, idempotent messages.

## Time

Time is the normal current time. Business dates and settlement cut-offs are counted in WAT (Africa/Lagos, UTC+1), and timestamps are ISO 8601 with `+01:00`. Nothing can move the clock.

Settlement and status checks run when they are due. To run one now:

```
beerus nip settle-now
beerus nip status <session_id>
```

## Start

1. Run a Postgres server. Beerus connects to `postgres:///postgres` by default; change `database_url` in `.beerus/local.yaml` if yours differs.
2. Build it and set it up:

```
go build -o beerus ./cmd/beerus
./beerus init
```

`beerus init` checks Postgres, writes the config and creates the CBN and NIBSS databases. It shows each step with the time it took. Run it again if it stops part-way; it carries on where it stopped.

3. Start it:

```
./beerus up
```

4. Open a second terminal and add your first bank. A new Beerus has no banks:

```
./beerus bank create --name "Bank A" --type commercial
```

5. Use the other commands.

`beerus up` starts every participant and stays in the foreground. It needs a finished `beerus init` first. Press Ctrl+C to stop it. It prints the ports and the control room link; `beerus info` prints them again. The state directory is `.beerus` in the current directory. Set `BEERUS_DIR` to use another one, and set it the same way in every terminal.

`beerus help` lists every command, and `beerus <command> --help` shows one command's usage.

`beerus compose` prints a Docker Compose file if you prefer containers. It has one service for each bank in the setup, so a new setup gives a stack with no banks. To start containers with banks, write a setup file, render the compose file from it, and give the file to the cbn container through the state folder:

```
beerus init --dir render -f my-setup.yaml
beerus compose --dir render > compose.yaml
mkdir state && cp my-setup.yaml state/setup.yaml
BEERUS_STATE=$PWD/state BEERUS_UID=$(id -u) BEERUS_GID=$(id -g) BEERUS_SETUP_FILE=/state/setup.yaml docker compose up -d --build --wait
```

`BEERUS_SETUP_FILE` is read once, when the state is first made. A file that is missing or invalid stops the cbn container with the reason.

## Setup file

The setup lives in `.beerus/setup.yaml`. It lists the banks and partners, and the NIP session settings. A new setup has no banks and no partners. You add your own; the example below has two.

```
apiVersion: beerus.dev/v1
kind: Setup
metadata:
  name: beerus
spec:
  start: "2026-11-04T08:00:00+01:00"
  nip:
    sessions: 4
    tsq_after: 2m
    tsq_attempts: 3
  banks:
    - id: bank-a
      name: Bank A
      type: commercial
    - id: bank-b
      name: Bank B
      type: digital
```

Edit the file or write your own, then:

```
beerus diff -f my-setup.yaml
beerus apply -f my-setup.yaml
beerus up -f my-setup.yaml
```

- `diff` shows the plan and changes nothing.
- `apply` makes the running system match the file. `--prune` also removes banks and partners that are not in the file.
- `up -f` starts from that file. Run `beerus init -f my-setup.yaml` first to make a new directory from it.
- `beerus get banks`, `beerus describe bank <id>`, `beerus export` and `beerus verify` show what is running.

You can also add banks and partners one at a time. After `beerus init` there are no banks, so add the first one:

```
beerus bank create --name "Bank A" --type commercial
beerus bank create --name "Bank B" --type microfinance
beerus partner add myapp --redirect-uri http://localhost:3000/callback --webhook-url http://localhost:3000/webhooks
beerus bank suspend bank-b
beerus bank resume bank-b
```

## Customers, accounts and cash

Pick a default bank with `beerus use bank-a`, or pass `--bank` to each command. Add `--json` to print the API response.

```
beerus customer create --name "Ada Obi" --phone 08030000000 --dob 1990-04-12
beerus account open --customer <customer_id> --type savings
beerus deposit <nuban> --amount 50000
beerus withdraw <nuban> --amount 1500.50
beerus statement <nuban>
beerus customers
beerus accounts
```

Amounts are in naira. Lists take `--limit` and `--cursor`.

## Transfers

```
beerus transfer --from <nuban> --to <nuban> --amount 2500 --from-bank bank-a --to-bank bank-b --narration "Rent"
beerus nip name-enquiry <bank_code> <nuban>
beerus nip participants
beerus nip positions
beerus nip settlement
```

A transfer between banks goes through the NIP switch and settles in a later session. Add `--dry-run` to `beerus transfer` to see the fee, VAT, stamp duty and total debit without sending.

## Faults

Faults make a bank misbehave so you can test your app.

```
beerus fault down --bank bank-b --for 1h
beerus fault timeout --bank bank-b --outcome unknown --count 2
beerus fault list
beerus fault clear --bank bank-b
```

A timed-out transfer has an outcome of `credited`, `failed` or `unknown`. Check it with `beerus nip status <session_id>`, which says when it found a pending session resolved.

## Removing a bank

Closing a bank needs a decision about its customers' money: `transfer` it to another bank, `payout` to the customers, or `leave` it. Only `transfer` takes `--to`: a payout is cash and needs no receiving bank. After every apply, the state directory's `setup.yaml` is rewritten to match what was applied, so a later `beerus up` starts from it.

```
beerus bank close bank-b --money transfer --to bank-a
beerus bank close bank-b --money payout --customer <customer_id>=leave
beerus bank purge bank-b --yes
```

When the bank has finished, Beerus prints what happened to each customer account: paid out in cash, transferred to an account at another bank, or left as an uninsured claim on a frozen account. `beerus describe bank <id>` shows the same after the bank is purged. If the decision is missing, or the receiving bank is down, the command stops at once and says what to do.

`close` moves the bank to the closed state and handles its customers' money as you chose. `purge` deletes the closed bank's database and cannot be undone. Both wait up to five minutes for the bank to finish; change this with `--wait`, and add `--settle-now` to run an extra settlement session. Removing a bank from the setup file and running `beerus apply` does the same work, with the decision in the bank's `on_remove` block.

## Partner apps

```
beerus partner list
beerus partner rotate-key <id>
beerus partner remove <id>
beerus connections
beerus revoke <connection-id>
beerus sync
beerus webhooks
```

## Housekeeping

```
beerus doctor [--fix]
beerus snapshot take
beerus reset <setup-name>
beerus trust
```

`doctor` finds leftover databases and snapshots. `reset` drops the setup's databases and state, and needs the setup's name as a check. `trust` adds the Beerus root certificate to your system.

## Standards

Open banking follows the CBN Operational Guidelines for Open Banking (March 2023): FAPI 2.0, mTLS, certificate-bound tokens, time-bound consent and a central registry. Interbank messages follow the structure of ISO 20022. These controls are enforced in every environment, including local development.

## License

Beerus is distributed under the terms in [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

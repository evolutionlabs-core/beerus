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

Beerus needs a Postgres server. The default is `postgres:///postgres`; change `database_url` in `.beerus/local.yaml` if yours differs.

```
go build -o beerus ./cmd/beerus
./beerus up
```

`beerus up` creates `.beerus` on the first run, starts every participant and stays in the foreground. It prints the ports and the control room link. Use `beerus info` to print them again. Run other commands from a second terminal, in the same directory or with `BEERUS_DIR` set.

`beerus compose > compose.yaml` prints a Docker Compose file if you prefer containers.

## Setup file

The setup lives in `.beerus/setup.yaml`. It lists the banks and partners, and the NIP session settings. A new setup has two banks, `bank-a` and `bank-b`.

```
apiVersion: beerus.dev/v1
kind: Setup
spec:
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
- `up -f` starts from that file. On a new directory it creates the setup from the file.
- `beerus get banks`, `beerus describe bank <id>`, `beerus export` and `beerus verify` show what is running.

You can also add banks and partners one at a time:

```
beerus bank create --name "Bank C" --type microfinance
beerus partner add myapp --redirect-uri http://localhost:3000/callback --webhook-url http://localhost:3000/webhooks
beerus bank suspend bank-c
beerus bank resume bank-c
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

A transfer between banks goes through the NIP switch and settles in a later session.

## Faults

Faults make a bank misbehave so you can test your app.

```
beerus fault down --bank bank-b --for 1h
beerus fault timeout --bank bank-b --outcome unknown --count 2
beerus fault list
beerus fault clear --bank bank-b
```

A timed-out transfer has an outcome of `credited`, `failed` or `unknown`. Check it with `beerus nip status <session_id>`.

## Removing a bank

Closing a bank needs a decision about its customers' money: `transfer` it to another bank, `payout` to the customers, or `leave` it.

```
beerus bank close bank-b --money transfer --to bank-a
beerus bank close bank-b --money payout --customer <customer_id>=leave
beerus bank purge bank-b --yes
```

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

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="web/brand/mark-dark.svg">
  <img src="web/brand/mark-light.svg" alt="" width="88">
</picture>

# Beerus

Beerus runs a copy of Nigeria's banking infrastructure on your machine: banks, NIBSS, CBN settlement and open banking, in one binary. Use it to build and test apps that move money between banks or read account data through open banking.

## Install

Needs Go 1.27 and a Postgres server at `postgres:///postgres`. If yours is elsewhere, set `BEERUS_DATABASE_URL`.

```bash
go build -o beerus ./cmd/beerus
```

## Quickstart

`beerus up` stays in the foreground, so start it in one terminal:

```bash
./beerus init
./beerus up
```

Run the rest in a second terminal, in the same folder:

```bash
./beerus bank create --name "Bank A"
./beerus bank create --name "Bank B" --type digital
./beerus customer create --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --bank bank-a
./beerus customer create --name "Tunde Bello" --phone 08037654321 --dob 1988-11-02 --bank bank-b
./beerus account open --customer ADA_ID --type current --bank bank-a
./beerus account open --customer TUNDE_ID --type savings --bank bank-b
./beerus deposit 0000000014 --amount 50000 --bank bank-a
./beerus transfer --from 0000000014 --from-bank bank-a --to 0000000011 --to-bank bank-b --amount 10000
./beerus nip settle-now
```

`ADA_ID` and `TUNDE_ID` are the `cus_` IDs that `customer create` prints. The transfer credits Tunde at once, and `settle-now` settles it between the banks:

```text
Session 000001261119115733719221795118: success at Thu 19 Nov 2026 11:57 WAT
Settled 1 session at Thu 19 Nov 2026 11:57 WAT.
```

## What runs

| Participant | What it does |
| --- | --- |
| CBN | Holds each bank's settlement account; root of trust |
| NIBSS | NIP switch, BVNs, settlement sessions, consent ledger |
| Banks | Own database, ledger, online banking, open banking API |
| Connect | Open banking aggregator with a partner API for your app |
| core | Starts participants, adds banks, serves the control API |

## Documentation

- [All documentation](docs/README.md)
- Start: [get started](docs/get-started.md)
- Do: [banks](docs/banks.md), [customers and accounts](docs/customers-and-accounts.md), [transfers](docs/transfers.md), [connect an app](docs/open-banking.md), [setup files](docs/setup-file.md), [failures](docs/faults.md), [Docker Compose](docs/docker.md), [reset and clean up](docs/housekeeping.md)
- Look up: [commands](docs/commands.md), [setup file reference](docs/setup-reference.md), [partner API](docs/partner-api.md), [charges and limits](docs/charges-and-limits.md)
- Understand: [how Beerus works](docs/how-beerus-works.md)

## Limits

- Some figures have no public source, such as net debit caps, daily limits and CBN opening balances. The docs list them as [placeholders](docs/charges-and-limits.md#placeholders).
- Only banks in your setup exist. A transfer to any other bank fails.
- Beerus runs on real Lagos time. Nothing is backdated, and no history is generated. Money moves only when you, your app or a settlement cutoff moves it.

## License

Beerus is distributed under the terms in [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

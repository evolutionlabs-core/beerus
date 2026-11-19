# Commands

Every `beerus` command, in `beerus help` order. `beerus <command> --help` prints the same options.

- Positional arguments come before flags: `beerus deposit 0000000014 --amount 500`. Flag first fails with `"--amount" should be a value, not a flag`. `health` is the exception: flags go before the URL.
- Only these commands take `--dir`: `up`, `init`, `apply`, `diff`, `get`, `describe`, `export`, `verify`, `reset`, `run`, `compose`, `info`, `doctor`, `trust`, `untrust`, `partner *`, `bank close`, `bank purge`. The rest read `BEERUS_DIR`. Set `BEERUS_DIR` once per terminal.
- Bank commands use `--bank ID`, or the bank chosen with `beerus use`.
- Amounts are naira: `25000` or `2500.50`.
- Lists take `--limit` (1 to 1000) and `--cursor` (the `next_cursor` from the last page).
- `--json` prints the API response as JSON.
- Errors print `beerus: <message>` on stderr.

| Exit code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Error |
| `2` | `diff` found changes |
| `3` | `apply`, `bank close` or `bank purge` wait ended blocked or timed out |

| Variable | Default | Meaning |
| --- | --- | --- |
| `BEERUS_DIR` | `.beerus` | State directory |
| `BEERUS_DATABASE_URL` | `local.yaml` value | Postgres URL; overrides `local.yaml` |
| `BEERUS_BIND` | `127.0.0.1` | Default for `run --bind` |
| `BEERUS_CORE_URL` | | Default for `run --core` |

Container variables are in [run with Docker Compose](docker.md).

## up

Start Beerus and apply your setup. Runs in the foreground; Ctrl+C stops it.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `-f FILE` | `<dir>/setup.yaml` | Setup file to start from or apply |
| `--prune` | off | Remove banks and partners not in the file |

```bash
beerus up
```

## bank

Create, suspend, close and remove banks. Bare `beerus bank` runs `bank list`. Task guide: [add and manage banks](banks.md).

### bank list

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state STATE` | all | `active`, `suspended`, `provisioning`, `repairing` or `closed` |

```bash
beerus bank list --state closed
```

### bank create

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name NAME` | required | Bank name |
| `--id ID` | from the name | Bank ID |
| `--type TYPE` | `commercial` | `commercial`, `digital` or `microfinance` |
| `--settles-through BANK` | first commercial bank | Bank a microfinance bank settles through |

```bash
beerus bank create --name "Bank B" --type digital
```

### bank suspend

`beerus bank suspend <id>`. No flags.

```bash
beerus bank suspend bank-b
```

### bank resume

`beerus bank resume <id>`. No flags.

```bash
beerus bank resume bank-b
```

### bank close

`beerus bank close <id> [options]`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--money CHOICE` | required | `transfer`, `payout` or `leave` |
| `--to BANK` | | Bank that receives transferred money |
| `--customer ID=CHOICE` | | Choice for one customer; repeatable |
| `--settle-now` | off | Run an extra NIP settlement session |
| `--wait DURATION` | `5m0s` | How long to wait; `0` does not wait |
| `--yes` | off | Skip the typed confirmation |
| `--json` | off | Print JSON |
| `--dir DIR` | `.beerus` | State directory |

```bash
beerus bank close bank-b --money transfer --to bank-a --yes
```

### bank purge

`beerus bank purge <id> [options]`. Deletes the bank's database. Same flags as `bank close`.

```bash
beerus bank purge bank-b --yes
```

## customer

Create and close customers. Bare `beerus customer` prints usage and exits 1.

### customer create

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name NAME` | required | Full name |
| `--phone PHONE` | required | Nigerian number, such as `08031234567` |
| `--dob DATE` | required | Date of birth, `YYYY-MM-DD` |
| `--username USERNAME` | | Online banking username; needs `--password-stdin` |
| `--password-stdin` | off | Read the online banking password from stdin |
| `--bank BANK` | session bank | Bank ID |
| `--json` | off | Print JSON |

```bash
printf '%s' 'PASSWORD' | beerus customer create --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password-stdin
```

`PASSWORD` is the online banking password.

### customer close

`beerus customer close <id> [options]`. Close the customer's accounts first.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--json` | off | Print JSON |

```bash
beerus customer close cus_icoerw7f6cnefmtsuv3g
```

## account

Open and close accounts. Bare `beerus account` prints usage and exits 1.

### account open

| Flag | Default | Meaning |
| --- | --- | --- |
| `--customer ID` | required | Customer ID |
| `--type TYPE` | required | `current` or `savings` |
| `--bank BANK` | session bank | Bank ID |
| `--json` | off | Print JSON |

```bash
beerus account open --customer cus_jmjyyf7yer62o4nemkhn --type current
```

### account close

`beerus account close <nuban> [options]`. The balance must be zero.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--json` | off | Print JSON |

```bash
beerus account close 0000000021
```

## transfer

Send money between accounts. Inside one bank it is an own-bank transfer; across banks it goes over NIP.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--from NUBAN` | required | Account to pay from |
| `--to NUBAN` | required | Account to pay into |
| `--amount NAIRA` | required | Amount in naira |
| `--from-bank ID` | session bank | Bank of the paying account |
| `--to-bank ID` | the paying bank | Bank of the receiving account |
| `--narration TEXT` | | Text shown on the transfer |
| `--dry-run` | off | Show charges and total debit; send nothing |
| `--idempotency-key KEY` | new key | Makes a rerun safe after a timeout |
| `--json` | off | Print JSON |

```bash
beerus transfer --from 0000000014 --to 0000000011 --to-bank bank-b --amount 10000 --narration 'Rent share'
```

## nip

Check NIP sessions and settle now. Bare `beerus nip` prints usage and exits 1. Every subcommand takes `--json`.

### nip status

`beerus nip status <session_id>`. Asks NIBSS for the session now.

```bash
beerus nip status 000001261119115733719221795118
```

### nip settle-now

Settle every open NIP session now.

```bash
beerus nip settle-now
```

### nip name-enquiry

`beerus nip name-enquiry <bank_code> <nuban>`. `bank_code` is the NIP code.

```bash
beerus nip name-enquiry 000002 0000000011
```

### nip participants

List the banks in the NIP directory.

```bash
beerus nip participants
```

### nip positions

Show each bank's NIP position and net debit cap.

```bash
beerus nip positions
```

### nip settlement

Check that NIP settlement balances.

```bash
beerus nip settlement
```

## init

Check Postgres and set up the state directory.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `-f FILE` | built-in setup | Setup file to start from |
| `--partner-port PORT` | `7443` | Partner API; banks take the next free ports |
| `--widget-port PORT` | `7444` | Connect widget |
| `--cbn-port PORT` | `7446` | CBN |
| `--control-port PORT` | `7480` | Control API |
| `--nibss-port PORT` | `7481` | NIBSS |
| `--connect-control-port PORT` | first free from `7482` | Connect control |
| `--health-port PORT` | `7490` | Health port of the cbn container |

```bash
beerus init
```

## apply

Apply the setup file to the running Beerus. See [describe a setup in a file](setup-file.md).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `-f FILE` | `<dir>/setup.yaml` | Setup file |
| `--prune` | off | Remove banks and partners not in the file |
| `--money CHOICE` | | `transfer`, `payout` or `leave` |
| `--to BANK` | | Bank that receives transferred money |
| `--customer ID=CHOICE` | | Choice for one customer; repeatable |
| `--settle-now` | off | Extra NIP session for closing or removed banks |
| `--wait DURATION` | `5m0s` | How long to wait; `0` does not wait |
| `--json` | off | Print JSON |

```bash
beerus apply -f my-setup.yaml
```

## diff

Show what `apply` would change. Exits 2 when there are changes. Works with Beerus stopped.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `-f FILE` | `<dir>/setup.yaml` | Setup file |
| `--json` | off | Print JSON |

```bash
beerus diff -f my-setup.yaml
```

## get

`beerus get banks|partners`. List banks or partners.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `--limit NUMBER` | | Page size, 1 to 1000 |
| `--cursor CURSOR` | | Next page |
| `--json` | off | Print JSON |

```bash
beerus get banks
```

## describe

`beerus describe bank|partner <id>`. Show one bank or partner in detail.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `--json` | off | Print JSON |

```bash
beerus describe bank bank-a
```

## export

Write the running setup as a file, without assigned codes.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `-o FILE` | stdout | Write to FILE (mode 0600) |
| `--json` | off | Print JSON; not with `-o` |

```bash
beerus export -o my-setup.yaml
```

## verify

Print the SHA-256 hash of the applied setup.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `--json` | off | Print JSON |

```bash
beerus verify
```

## reset

`beerus reset <setup-name>`. Drop the setup's databases and clear the state directory, keeping `local.yaml`. Refuses while Beerus runs. See [reset and clean up](housekeeping.md).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `--json` | off | Print the dropped databases as JSON |

```bash
beerus reset beerus
```

## use

`beerus use <bank>`. Open a sub-shell where later commands use this bank. Type `exit` to leave. Inside the sub-shell, `use` switches bank. No flags.

```bash
beerus use bank-a
```

## customers

List a bank's customers.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--limit NUMBER` | | Page size, 1 to 1000 |
| `--cursor CURSOR` | | Next page |
| `--json` | off | Print JSON |

```bash
beerus customers --bank bank-b
```

## accounts

List a bank's accounts.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--customer CUSTOMER` | | Only this customer's accounts |
| `--limit NUMBER` | | Page size, 1 to 1000 |
| `--cursor CURSOR` | | Next page |
| `--json` | off | Print JSON |

```bash
beerus accounts --bank bank-b
```

## deposit

`beerus deposit <nuban> --amount NAIRA`. Put cash into an account.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--amount NAIRA` | required | Amount in naira |
| `--bank BANK` | session bank | Bank ID |
| `--idempotency-key KEY` | new key | Makes a rerun safe after a timeout |
| `--json` | off | Print JSON |

```bash
beerus deposit 0000000014 --amount 50000
```

## withdraw

`beerus withdraw <nuban> --amount NAIRA`. Take cash out of an account. Counts toward the daily limit.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--amount NAIRA` | required | Amount in naira |
| `--bank BANK` | session bank | Bank ID |
| `--idempotency-key KEY` | new key | Makes a rerun safe after a timeout |
| `--json` | off | Print JSON |

```bash
beerus withdraw 0000000014 --amount 1500.50
```

## statement

`beerus statement <nuban>`. Show an account's statement.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--from DATE` | | First date, `YYYY-MM-DD` |
| `--to DATE` | | Last date, `YYYY-MM-DD` |
| `--limit NUMBER` | | Page size, 1 to 1000 |
| `--cursor CURSOR` | | Next page |
| `--json` | off | Print JSON |

```bash
beerus statement 0000000014 --from 2026-11-19 --limit 20
```

## partner

Add, list, rekey and remove partner apps. Every subcommand takes `--dir DIR`. Bare `beerus partner` prints usage and exits 1.

### partner add

`beerus partner add <id> --redirect-uri URL`. ID: lowercase letters, digits and dashes, up to 40 characters.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--redirect-uri URL` | required | Where the widget returns the browser; repeatable |
| `--webhook-url URL` | | Where webhooks are sent |
| `--name NAME` | the ID | App name on the consent screen |
| `--legal-name NAME` | the name | Legal name on the consent screen |
| `--purpose PURPOSE` | `to read your account details, balances and transactions` | Purpose on the consent screen |

```bash
beerus partner add balance --redirect-uri http://localhost:3000/bank_connections/callback --webhook-url http://localhost:3000/webhooks/beerus
```

### partner list

```bash
beerus partner list
```

### partner rotate-key

`beerus partner rotate-key <id>`. Issues a new API key and certificate. The old ones stop working.

```bash
beerus partner rotate-key balance
```

### partner remove

`beerus partner remove <id>`.

```bash
beerus partner remove balance
```

## registry

Open banking clients and consents in the NIBSS registry. Bare `beerus registry` runs `registry list`.

### registry list

List registered clients. No flags.

```bash
beerus registry list
```

### registry add-client

`beerus registry add-client <id> --cert-cn CN --public-key FILE --redirect-uri URL`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--cert-cn CN` | required | CN of the client's mTLS certificate |
| `--public-key FILE` | required | PEM file with the client's EC P-256 public key |
| `--redirect-uri URL` | required | Where the bank returns the browser; repeatable |
| `--name NAME` | | Brand name |
| `--legal-name NAME` | | Legal name |
| `--cac CAC` | | CAC number |

```bash
beerus registry add-client acme --cert-cn acme.localhost --public-key ec.pub --redirect-uri http://localhost:4000/callback --legal-name 'Acme Ltd' --name Acme
```

### registry consents

List consent changes.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | all banks | Only changes recorded by this bank |

```bash
beerus registry consents
```

## connections

List partner connections. No flags.

```bash
beerus connections
```

## revoke

`beerus revoke <connection-id>`. Revoke a connection at the bank. No flags.

```bash
beerus revoke con_tb9egky6ey7v163w
```

## sync

Sync all connections now. No flags.

```bash
beerus sync
```

## webhooks

List webhook deliveries and replay one. Bare `beerus webhooks` runs `webhooks list`.

### webhooks list

| Flag | Default | Meaning |
| --- | --- | --- |
| `--limit NUMBER` | | Page size, 1 to 1000 |
| `--cursor CURSOR` | | Next page |

```bash
beerus webhooks list --limit 2
```

### webhooks replay

`beerus webhooks replay <delivery-id>`. Send a delivery again now. No flags.

```bash
beerus webhooks replay msg_ekga1az4egat9zfx
```

## fault

Make a bank go down or time out. Bare `beerus fault` prints usage and exits 1. Every subcommand takes `--json`.

### fault down

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | session bank | Bank ID |
| `--for DURATION` | until cleared | How long, up to `24h` |

```bash
beerus fault down --bank bank-b --for 10m
```

### fault timeout

`beerus fault timeout --outcome credited|failed|unknown`.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--outcome OUTCOME` | required | What really happened: `credited`, `failed` or `unknown` |
| `--count NUMBER` | `1` | Number of transfers that time out |
| `--bank BANK` | session bank | Bank ID |

```bash
beerus fault timeout --bank bank-b --outcome credited --count 1
```

### fault list

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | every bank | Bank ID |

```bash
beerus fault list
```

### fault clear

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bank BANK` | every bank | Bank ID |

Without `--bank`, `fault clear` clears every bank's faults, also inside `beerus use`.

```bash
beerus fault clear --bank bank-b
```

## run

`beerus run --participant NAME`. Run one participant. `beerus up` and Docker Compose use it.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--participant NAME` | required | `cbn`, `core`, `nibss`, `connect` or a bank ID |
| `--bind ADDRESS` | `127.0.0.1` | Address to listen on |
| `--core URL` | | Core control API URL, for banks |
| `--dir DIR` | `.beerus` | State directory |
| `--supervised` | off | Stop when stdin closes |

```bash
beerus run --participant bank-a
```

## compose

Print a Docker Compose file for the setup.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |

```bash
beerus compose --dir render > compose.ci.yaml
```

## info

Show addresses and partner apps.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |

```bash
beerus info
```

## doctor

Find leftover databases and status entries.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |
| `--fix` | off | Drop leftover databases and forget removed banks |

```bash
beerus doctor
```

## trust

Add the Beerus root certificate to the system trust store. macOS and Linux only.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |

```bash
beerus trust
```

## untrust

Remove the Beerus root certificate from the system trust store.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir DIR` | `.beerus` | State directory |

```bash
beerus untrust
```

## health

`beerus health [options] <url>`. Exits 0 when the URL answers below 300. Otherwise prints `<url> answered <status>` and exits 1. Put flags before the URL.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--token-file FILE` | | File with a bearer token |

```bash
beerus health --token-file .beerus/control-token http://127.0.0.1:7480/health
```

## version

Print the version. No flags.

```bash
beerus version
```

```text
beerus 0.1.0
```

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="web/brand/mark-dark.svg">
  <img src="web/brand/mark-light.svg" alt="" width="88">
</picture>

# Beerus

Nigeria's banking infrastructure on your machine: banks, NIBSS, CBN settlement and open banking, in one binary. Build and test apps that move money or read bank data.

Banks start empty. Money moves only when you or your app move it. Time is real Lagos time.

## Install

Beerus needs a running Postgres server. It connects to `postgres:///postgres`; set `BEERUS_DATABASE_URL` if yours is elsewhere.

macOS and Linux, with Homebrew:

```bash
brew install evolutionlabs-core/tap/beerus
```

Update with `brew upgrade beerus`.

If you ran `beerus trust` on v0.1.0, remove the old root: `security delete-certificate -c "Beerus CBN Trust Anchor" ~/Library/Keychains/login.keychain-db`. You can also delete the old certificate files: `ca/` and the `*.crt` and `*.key` files under the Beerus directory.

Without Homebrew, download the archive for your system from [Releases](https://github.com/evolutionlabs-core/beerus/releases), unpack it and put `beerus` on your `PATH`.

## Start

```bash
beerus init
beerus up
```

`up` stays running. Use a second terminal for everything else. Ctrl+C stops it.

```bash
beerus info
```

Prints every address and the control room link.

## Banks

```bash
beerus bank create --name "Bank A"
beerus bank create --name "Bank B" --type digital
beerus bank create --name "Lapo MFB" --type microfinance --settles-through bank-a
beerus get banks
beerus describe bank bank-a
beerus bank suspend bank-b
beerus bank resume bank-b
```

Types: `commercial`, `digital`, `microfinance`.

## Customers and accounts

```bash
printf '%s' 'PASSWORD' | beerus customer create --bank bank-a --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password-stdin
beerus account open --bank bank-a --customer CUSTOMER_ID --type current
beerus customers --bank bank-a
beerus accounts --bank bank-a
```

`--username` gives the customer an online banking login. Leave it and `--password-stdin` out if you don't need one.

Skip `--bank` on every command:

```bash
beerus use bank-a
```

## Cash

```bash
beerus deposit NUBAN --bank bank-a --amount 50000
beerus withdraw NUBAN --bank bank-a --amount 1500.50
beerus statement NUBAN --bank bank-a
```

Amounts are in naira.

## Transfers

See the charges first:

```bash
beerus transfer --from NUBAN --from-bank bank-a --to NUBAN --to-bank bank-b --amount 10000 --dry-run
```

```text
Charges: fee ₦10.00, VAT ₦0.75 and stamp duty ₦50.00. Total debit ₦10,060.75.
```

Send it:

```bash
beerus transfer --from NUBAN --from-bank bank-a --to NUBAN --to-bank bank-b --amount 10000 --narration "Rent"
beerus nip status SESSION_ID
beerus nip name-enquiry 000002 NUBAN
```

| Amount | Fee | VAT | Stamp duty |
| --- | --- | --- | --- |
| Up to ₦5,000 | ₦0 | ₦0 | ₦0 |
| ₦5,001 to ₦9,999 | ₦10 | ₦0.75 | ₦0 |
| ₦10,000 to ₦50,000 | ₦10 | ₦0.75 | ₦50 |
| Above ₦50,000 | ₦50 | ₦3.75 | ₦50 |

Sources: CBN 2026 draft Guide to Charges, Nigeria Tax Act 2025.

## Settlement

The receiver is credited at once. Banks settle with each other later, through their CBN accounts, at 06:00, 12:00, 18:00 and 24:00 on business days.

```bash
beerus nip positions
beerus nip settle-now
beerus nip settlement
```

## Create faults

```bash
beerus fault down --bank bank-b --for 10m
beerus fault timeout --bank bank-b --outcome credited
beerus fault list
beerus fault clear --bank bank-b
```

Timeout outcomes: `credited`, `failed`, `unknown`. Check a timed-out transfer with `beerus nip status`.

## Close a bank

Choose what happens to customers' money: `transfer` to another bank, `payout` in cash, or `leave` it frozen.

```bash
beerus bank close bank-b --money transfer --to bank-a --yes
beerus bank purge bank-b --yes
```

Purge deletes the bank's database. It cannot be undone.

## Setup file

Save the running setup, edit it, then apply it:

```bash
beerus export -o my-setup.yaml
beerus diff -f my-setup.yaml
beerus apply -f my-setup.yaml
```

```yaml
apiVersion: beerus.dev/v1
kind: Setup
metadata:
  name: beerus
spec:
  nip:
    sessions: 4
    tsq_after: 2m
  banks:
    - id: bank-a
      name: Bank A
      type: commercial
```

Start a new Beerus from a file with `beerus init -f my-setup.yaml`.

## Clean up

```bash
beerus doctor
beerus reset beerus
```

`reset` drops the setup's databases and keys. It cannot be undone.

---

# Beerus Connect

Connect your existing app to Beerus, the way you would connect it to Mono or Okra. Your customers link their bank accounts through Beerus Connect. Your app then reads their accounts, balances and transactions, and gets webhooks when something changes.

## 1. Register your app

```bash
beerus partner add myapp --redirect-uri http://localhost:3000/callback --webhook-url http://localhost:3000/webhooks
```

Your app's API key lands in `.beerus/partners/myapp`. Every call to the Partner API at `http://connect.localhost:7443` sends it in the `X-Api-Key` header. To try calls from a terminal:

```bash
P=.beerus/partners/myapp
api() { curl -sS -H "X-Api-Key: $(cat $P/api-key)" -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" "http://connect.localhost:7443$1" "${@:2}"; echo; }
```

## 2. Let a customer link their bank

Your app creates a link session:

```bash
api /v1/link_sessions -d '{"start_date":"2026-11-01","workspace_id":"ws_1","redirect_uri":"http://localhost:3000/callback"}'
```

Send the customer to its `widget_url`. They pick their bank, log in with their online banking details and approve. Beerus Connect then sends them to your redirect URI with a `code`.

## 3. Get the connection

Exchange the code within 10 minutes:

```bash
api /v1/connections/exchange -d '{"code":"CODE","link_session_id":"LINK_SESSION_ID"}'
```

## 4. Read customer data

```bash
api /v1/connections/CONNECTION_ID/accounts
api /v1/accounts/ACCOUNT_ID/balance
api /v1/accounts/ACCOUNT_ID/transactions/sync
```

## Manage it

```bash
beerus connections
beerus sync
beerus webhooks
beerus revoke CONNECTION_ID
beerus partner rotate-key myapp
```

The customer needs an online banking login: create them with `--username`. Full API: [api/partner/openapi.yaml](api/partner/openapi.yaml).

---

## More

- `beerus help` lists every command. `beerus <command> --help` shows its flags.
- State lives in `.beerus`. Use another folder with `BEERUS_DIR=path`.
- Docker: `beerus compose > compose.yaml` prints a Compose file for your setup.
- Placeholders: net debit caps, daily limits and CBN opening balances are invented figures, not CBN or NIBSS values.

## License

AGPL-3.0. See [LICENSE](LICENSE).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="web/brand/mark-dark.svg">
  <img src="web/brand/mark-light.svg" alt="" width="88">
</picture>

# Beerus

Nigeria's banking infrastructure on your machine: banks, NIBSS, CBN settlement and open banking, in one binary. Build and test apps that move money or read bank data.

Banks start empty. Money moves only when you or your app move it. Time is real Lagos time.

## Contents

- [Install](#install)
- [Start](#start)
- [Usage](#usage)
  - [Banks](#banks)
  - [Customers and accounts](#customers-and-accounts)
  - [Cash](#cash)
  - [Transfers](#transfers)
  - [Settlement](#settlement)
  - [Faults](#faults)
  - [Close a bank](#close-a-bank)
  - [Setup file](#setup-file)
  - [Clean up](#clean-up)
- [Connect](#connect)
  - [How it works](#how-it-works)
  - [1. Register your app](#1-register-your-app)
  - [2. Link a customer's bank](#2-link-a-customers-bank)
  - [3. Exchange the public token](#3-exchange-the-public-token)
  - [4. Read their data](#4-read-their-data)
  - [Manage connections](#manage-connections)
  - [Good to know](#good-to-know)
- [Reference](#reference)
- [License](#license)

## Install

Beerus needs a running Postgres server. It connects to `postgres:///postgres`; set `BEERUS_DATABASE_URL` if yours is elsewhere.

macOS and Linux, with Homebrew:

```bash
brew install evolutionlabs-core/tap/beerus
```

Update with `brew upgrade beerus`.

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

## Usage

`beerus help` lists every command. `beerus <command> --help` shows its flags.

### Banks

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

### Customers and accounts

```bash
printf '%s' 'PASSWORD' | beerus customer create --bank bank-a --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password-stdin
beerus account open --bank bank-a --customer CUSTOMER_ID --type current
beerus customers --bank bank-a
beerus accounts --bank bank-a
```

`--username` gives the customer an online banking login, which they need to link their bank through [Connect](#connect). Leave it and `--password-stdin` out if you don't need one.

Skip `--bank` on every command:

```bash
beerus use bank-a
```

### Cash

```bash
beerus deposit NUBAN --bank bank-a --amount 50000
beerus withdraw NUBAN --bank bank-a --amount 1500.50
beerus statement NUBAN --bank bank-a
```

Amounts are in naira.

### Transfers

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

### Settlement

The receiver is credited at once. Banks settle with each other later, through their CBN accounts, at 06:00, 12:00, 18:00 and 24:00 on business days.

```bash
beerus nip positions
beerus nip settle-now
beerus nip settlement
```

### Faults

```bash
beerus fault down --bank bank-b --for 10m
beerus fault timeout --bank bank-b --outcome credited
beerus fault list
beerus fault clear --bank bank-b
```

Timeout outcomes: `credited`, `failed`, `unknown`. Check a timed-out transfer with `beerus nip status`.

### Close a bank

Choose what happens to customers' money: `transfer` to another bank, `payout` in cash, or `leave` it frozen.

```bash
beerus bank close bank-b --money transfer --to bank-a --yes
beerus bank purge bank-b --yes
```

Purge deletes the bank's database. It cannot be undone.

### Setup file

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

### Clean up

```bash
beerus doctor
beerus reset beerus
```

`reset` drops the setup's databases and keys. It cannot be undone.

## Connect

Beerus Connect lets your app read a customer's bank data, the way Mono or Okra do. The customer links their bank, and your app reads their accounts, balances and transactions and gets webhooks when something changes.

### How it works

1. Your server creates a **link token**.
2. Your page opens Connect with it. The customer signs in at their bank and picks accounts. Connect hands your page a **public token**.
3. Your server exchanges the public token for an **access token**, and reads the data with it.

Every call to the Partner API at `http://connect.localhost:7443` sends these headers:

| Header | Value | Sent on |
| --- | --- | --- |
| `Beerus-Client-ID` | your app's ID | every call |
| `Beerus-Secret` | your app's secret | every call |
| `Beerus-Access-Token` | the connection's access token | reading or removing a connection |
| `Idempotency-Key` | a new unique value | every `POST` |

### 1. Register your app

```bash
beerus partner add myapp --redirect-uri http://localhost:3000/callback --webhook-url http://localhost:3000/webhooks
```

The secret is saved in `.beerus/partners/myapp/secret`. To follow the examples below from a terminal:

```bash
URL=http://connect.localhost:7443
AUTH=(-H "Beerus-Client-ID: myapp" -H "Beerus-Secret: $(cat .beerus/partners/myapp/secret)")
```

The examples make each `Idempotency-Key` with `uuidgen`, which comes with macOS and most Linux systems.

List the banks a customer can link, for your own bank picker:

```bash
curl -s "$URL/v1/institutions?limit=50" "${AUTH[@]}"
```

### 2. Link a customer's bank

Create a link token. `user_id` is your own ID for the customer. `institution_id` is optional and opens Connect straight on that bank.

```bash
curl -s "$URL/v1/link/token/create" "${AUTH[@]}" \
  -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" \
  -d '{"user_id":"user_1","institution_id":"bank-a","redirect_uri":"http://localhost:3000/callback"}'
```

Open Connect on your page with the `link_token` from the response:

```html
<script src="http://connect.localhost:7444/connect.js"></script>
<script>BeerusConnect.open(LINK_TOKEN, {
  onSuccess: (publicToken, metadata) => fetch('/exchange', {method: 'POST', body: JSON.stringify({public_token: publicToken})}),
  onExit: error => {}
})</script>
```

- `onSuccess(publicToken, metadata)` runs when the customer finishes. `metadata` holds the `institution` and the `accounts` they picked.
- `onExit(error)` runs when they close Connect (`error` is `null`) or it fails (`error` has a `code` and `message`).

Without the script, send the customer to `http://connect.localhost:7444/link?token=LINK_TOKEN`. Connect redirects them back to your `redirect_uri` with a `public_token`.

### 3. Exchange the public token

```bash
curl -s "$URL/v1/public_token/exchange" "${AUTH[@]}" \
  -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" \
  -d '{"public_token":"PUBLIC_TOKEN"}'
```

The response holds an `access_token` and a `connection_id`. Keep the `access_token` on your server: Connect shows it only once.

### 4. Read their data

```bash
TOKEN=(-H "Beerus-Access-Token: ACCESS_TOKEN")
curl -s "$URL/v1/accounts" "${AUTH[@]}" "${TOKEN[@]}"
curl -s "$URL/v1/accounts/ACCOUNT_ID/balance?as_of=2026-01-31" "${AUTH[@]}" "${TOKEN[@]}"
curl -s "$URL/v1/accounts/ACCOUNT_ID/transactions/sync" "${AUTH[@]}" "${TOKEN[@]}"
```

### Manage connections

Remove a connection from your app:

```bash
curl -s -X POST "$URL/v1/connections/remove" "${AUTH[@]}" "${TOKEN[@]}" -H "Idempotency-Key: $(uuidgen)"
```

From the command line:

```bash
beerus connections
beerus sync
beerus webhooks
beerus revoke CONNECTION_ID
beerus partner rotate-key myapp
```

`partner rotate-key` issues a new secret, and the old one stops working at once.

### Good to know

- A `public_token` works once and lasts 10 minutes.
- The page that opens Connect must be served from the origin of the `redirect_uri` you registered. Connect only lets that origin frame it.
- Only the accounts the customer picked appear in your data. History starts 90 days before today, or less if the bank keeps less.
- An account outside the connection answers `404`. A removed connection's `access_token` answers `401`.
- After a reauth (`POST /v1/connection/reauth`), the exchange returns only the `connection_id`, and the existing `access_token` keeps working.
- Full API: [api/partner/openapi.yaml](api/partner/openapi.yaml).

## Reference

- State lives in `.beerus`. Use another folder with `BEERUS_DIR=path`.
- Postgres: set `BEERUS_DATABASE_URL` if it isn't at `postgres:///postgres`.
- Docker: `beerus compose > compose.yaml` prints a Compose file for your setup.
- Placeholders: net debit caps, daily limits and CBN opening balances are invented figures, not CBN or NIBSS values.

## License

AGPL-3.0. See [LICENSE](LICENSE).

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

Your app's client ID and secret land in `.beerus/partners/myapp`. Every call to the Partner API at `http://connect.localhost:7443` sends them in the `Beerus-Client-ID` and `Beerus-Secret` headers. To try calls from a terminal:

```bash
P=.beerus/partners/myapp
api() { curl -sS -H "Beerus-Client-ID: myapp" -H "Beerus-Secret: $(cat $P/secret)" ${TOKEN:+-H "Beerus-Access-Token: $TOKEN"} -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" "http://connect.localhost:7443$1" "${@:2}"; echo; }
```

To list the banks your customers can link, for your own bank picker:

```bash
api "/v1/institutions?limit=50"
```

## 2. Let a customer link their bank

Your app creates a link token. `user_id` is your own ID for the customer. `institution_id` is optional; give one from `GET /v1/institutions` to open Connect on that bank.

```bash
api /v1/link/token/create -d '{"user_id":"user_1","institution_id":"bank-a","redirect_uri":"http://localhost:3000/callback"}'
```

The response holds a `link_token` and its `expiration`. Open Connect over your page with one script tag and one call:

```html
<script src="http://connect.localhost:7444/connect.js"></script>
<script>BeerusConnect.open(LINK_TOKEN, {
  onSuccess: (publicToken, metadata) => fetch('/exchange', {method: 'POST', body: JSON.stringify({public_token: publicToken})}),
  onExit: error => {}
})</script>
```

Connect opens on the bank list, or goes straight to the bank when the link token has an `institution_id`. The customer logs in on the bank's own page in a popup and approves. Then:

- `onSuccess(publicToken, metadata)` runs when they finish. `publicToken` works once and lasts 10 minutes. `metadata` holds `institution` and `accounts` (`account_id`, `name` and `mask` for each account the customer picked).
- `onExit(error)` runs when they close Connect (`error` is `null`) or it fails (`error` has a `code` and `message`).

The page that calls `open` must be served from the origin of the `redirect_uri` you registered with `beerus partner add --redirect-uri`. Connect only lets that origin frame it, and sends results only to it. To keep the handle, use `const connect = BeerusConnect.create({token, onSuccess, onExit}); connect.open();`.

Without the script, send the customer to `http://connect.localhost:7444/link?token=LINK_TOKEN`. Beerus Connect then redirects them to your redirect URI with a `public_token` and a `link_session_id`. The customer picks which of the bank's accounts to share, and only those accounts appear in your data. Connect shares history from 90 days before today, or less if the bank keeps less.

## 3. Get the connection

Exchange the `public_token` within 10 minutes:

```bash
api /v1/public_token/exchange -d '{"public_token":"PUBLIC_TOKEN"}'
```

The response holds an `access_token` and a `connection_id`. Keep the `access_token` on your server: Beerus shows it once and your app sends it in the `Beerus-Access-Token` header, never in a URL. After a reauth (`POST /v1/connection/reauth`), exchanging the new `public_token` returns only the `connection_id`, and the existing `access_token` keeps working.

## 4. Read customer data

Set `TOKEN` to the `access_token`. `api` then sends it in the `Beerus-Access-Token` header.

```bash
TOKEN=ACCESS_TOKEN
api /v1/accounts
api "/v1/accounts/ACCOUNT_ID/balance?as_of=2026-01-31"
api /v1/accounts/ACCOUNT_ID/transactions/sync
```

An account outside the connection answers 404.

## 5. Remove the connection

```bash
api /v1/connections/remove -X POST
```

The connection ends and its `access_token` stops working (401).

## Manage it

```bash
beerus connections
beerus sync
beerus webhooks
beerus revoke CONNECTION_ID
beerus partner rotate-key myapp
```

`partner rotate-key` issues a new secret and the old one stops working at once.

The customer needs an online banking login: create them with `--username`. Full API: [api/partner/openapi.yaml](api/partner/openapi.yaml).

---

## More

- `beerus help` lists every command. `beerus <command> --help` shows its flags.
- State lives in `.beerus`. Use another folder with `BEERUS_DIR=path`.
- Docker: `beerus compose > compose.yaml` prints a Compose file for your setup.
- Placeholders: net debit caps, daily limits and CBN opening balances are invented figures, not CBN or NIBSS values.

## License

AGPL-3.0. See [LICENSE](LICENSE).

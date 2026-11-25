<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="web/brand/logo-dark.svg">
    <img src="web/brand/logo-light.svg" alt="Beerus" width="88">
  </picture>
</p>

# Beerus

Beerus is a local sandbox of Nigeria's banking system. It runs banks, the NIBSS payment switch and CBN settlement, plus an open banking aggregator your app can connect to and read customer data. Build and test financial apps without real money.

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
  - [Walkthrough](#walkthrough)
- [Connect](#connect)
  - [How it works](#how-it-works)
  - [Before you start](#before-you-start)
  - [1. Create a link token (server)](#1-create-a-link-token-server)
  - [2. Open Connect (frontend)](#2-open-connect-frontend)
  - [3. Exchange the public token (server)](#3-exchange-the-public-token-server)
  - [4. Read the data (server)](#4-read-the-data-server)
  - [Manage connections](#manage-connections)
- [License](#license)

## Install

Requires: Postgres. If yours isn't at `postgres:///postgres`, set `BEERUS_DATABASE_URL`.

```bash
brew install evolutionlabs-core/tap/beerus
```

Or download it from [Releases](https://github.com/evolutionlabs-core/beerus/releases).

## Start

Create a Beerus, start it, then print its addresses from a second terminal.

```bash
beerus init
beerus up
beerus info
```

## Usage

`beerus help` lists every command. `beerus <command> --help` shows its flags.

### Banks

Create, list, inspect, suspend and resume banks, and pick a default bank so later commands can skip `--bank`.

```bash
beerus bank create --name "Bank A"
beerus bank create --name "Bank B" --type digital
beerus bank create --name "Lapo MFB" --type microfinance --settles-through bank-a
beerus get banks
beerus describe bank bank-a
beerus bank suspend bank-b
beerus bank resume bank-b
beerus use bank-a
```

### Customers and accounts

Create customers and open accounts for them.

```bash
beerus customer create --bank bank-a --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password ada123
beerus account open --bank bank-a --customer CUSTOMER_ID --type current
beerus customers --bank bank-a
beerus accounts --bank bank-a
```

### Cash

Deposit and withdraw cash, and see an account's statement.

```bash
beerus deposit NUBAN --bank bank-a --amount 50000
beerus withdraw NUBAN --bank bank-a --amount 1500.50
beerus statement NUBAN --bank bank-a
```

### Transfers

Send money to another account, at the same bank or another, and check a transfer or an account name.

```bash
beerus transfer --from NUBAN --from-bank bank-a --to NUBAN --to-bank bank-b --amount 10000 --dry-run
beerus transfer --from NUBAN --from-bank bank-a --to NUBAN --to-bank bank-b --amount 10000 --narration "Rent"
beerus nip status SESSION_ID
beerus nip name-enquiry 000002 NUBAN
```

### Settlement

See what banks owe each other, and settle it now.

```bash
beerus nip positions
beerus nip settle-now
beerus nip settlement
```

> [!NOTE]
> Net debit caps, daily limits and CBN opening balances are invented placeholder figures, not real CBN or NIBSS values.

### Faults

Make a bank go down or time out, to test how your app handles it.

```bash
beerus fault down --bank bank-b --for 10m
beerus fault timeout --bank bank-b --outcome credited
beerus fault list
beerus fault clear --bank bank-b
```

### Close a bank

Close a bank, move its customers' money to another bank, then delete it.

```bash
beerus bank close bank-b --money transfer --to bank-a --yes
beerus bank purge bank-b --yes
```

> [!CAUTION]
> `bank purge` deletes the bank's database, with all its customers, accounts and history. It cannot be undone.

### Setup file

Save your setup to a file, edit it, and apply the changes.

```bash
beerus export -o my-setup.yaml
beerus diff -f my-setup.yaml
beerus apply -f my-setup.yaml
beerus init -f my-setup.yaml
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

### Clean up

Check your install, and delete everything this Beerus created.

```bash
beerus doctor
beerus reset beerus
```

> [!CAUTION]
> `reset` deletes all of this setup's databases and clears the `.beerus` folder. It cannot be undone.

### Walkthrough

Two banks, one customer each, and a transfer between them.

**1. Create two banks**

```bash
beerus bank create --name "Bank A"
beerus bank create --name "Bank B" --type digital
```

**2. Create a customer at each bank**

```bash
beerus customer create --bank bank-a --name "Ada Obi" --phone 08031234567 --dob 1990-05-14
beerus customer create --bank bank-b --name "Tunde Bello" --phone 08039876543 --dob 1988-11-02
```

**3. Open an account for each customer**

```bash
beerus account open --bank bank-a --customer ADA_ID --type savings
beerus account open --bank bank-b --customer TUNDE_ID --type current
```

**4. Deposit money into Ada's account**

```bash
beerus deposit ADA_NUBAN --bank bank-a --amount 50000
```

**5. Send ₦10,000 from Ada to Tunde**

```bash
beerus transfer --from ADA_NUBAN --from-bank bank-a --to TUNDE_NUBAN --to-bank bank-b --amount 10000 --narration "Rent"
```

**6. View Tunde's statement**

```bash
beerus statement TUNDE_NUBAN --bank bank-b
```

## Connect

Beerus Connect is an open banking aggregator, like Mono or Plaid. It connects every Beerus bank in one place, so your app can link a customer's bank and read their accounts, balances and transactions.

### How it works

1. Your server creates a `link_token` and passes it to your page.
2. Your page opens Connect with the `link_token`. When the customer has linked their bank, Connect gives you a `public_token`.
3. Your server exchanges the `public_token` for an `access_token`.
4. Your server uses the `access_token` to read the customer's data.

### Before you start

Register your app. Its secret is saved in `.beerus/partners/myapp/secret`.

```bash
beerus partner add myapp --redirect-uri http://localhost:3000/callback --webhook-url http://localhost:3000/webhooks
```

### 1. Create a link token (server)

```bash
curl -X POST http://connect.localhost:7443/v1/link/token/create \
  -H "Beerus-Client-ID: myapp" \
  -H "Beerus-Secret: YOUR_SECRET" \
  -H "Idempotency-Key: UNIQUE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"user_id": "user_1", "redirect_uri": "http://localhost:3000/callback"}'
```

### 2. Open Connect (frontend)

```html
<script src="http://connect.localhost:7444/connect.js"></script>
<script>
  BeerusConnect.open("LINK_TOKEN", {
    onSuccess: (publicToken, metadata) => {
      // send publicToken to your server
    },
    onExit: (error) => {},
  });
</script>
```

### 3. Exchange the public token (server)

```bash
curl -X POST http://connect.localhost:7443/v1/public_token/exchange \
  -H "Beerus-Client-ID: myapp" \
  -H "Beerus-Secret: YOUR_SECRET" \
  -H "Idempotency-Key: UNIQUE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"public_token": "PUBLIC_TOKEN"}'
```

Save the `access_token` from the response. Connect shows it only once.

### 4. Read the data (server)

```bash
curl http://connect.localhost:7443/v1/accounts \
  -H "Beerus-Client-ID: myapp" \
  -H "Beerus-Secret: YOUR_SECRET" \
  -H "Beerus-Access-Token: ACCESS_TOKEN"
```

Balances: `GET /v1/accounts/ACCOUNT_ID/balance`. Transactions: `GET /v1/accounts/ACCOUNT_ID/transactions/sync`.

### Manage connections

```bash
beerus connections
beerus webhooks
beerus revoke CONNECTION_ID
beerus partner rotate-key myapp
```

Full API: [Beerus Connect API](api/partner/README.md).

## License

AGPL-3.0. See [LICENSE](LICENSE).

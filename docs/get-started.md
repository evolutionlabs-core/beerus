# Get started

Go from nothing to a settled transfer between two banks.

Needs: Go 1.27 and a running Postgres server.

## Install

1. Build the binary from the repository root.

   ```bash
   go build -o beerus ./cmd/beerus
   ```

2. Set up the state directory.

   ```bash
   ./beerus init
   ```

   ```text
   Checking Postgres                       ✓  0.0s
   Writing config                          ✓  0.0s
   Reserving CBN and NIBSS database names  ✓  0.0s
   Preparing CBN ports and certificates    ✓  0.0s

   Beerus is ready in .beerus (0.0s). Start it with beerus up, then add a bank with beerus bank create --name NAME.
   ```

   `init` connects to `postgres:///postgres`. If your Postgres is elsewhere, set `BEERUS_DATABASE_URL`, or edit `database_url` in `.beerus/local.yaml`. Run `init` again if it stops part-way. It carries on where it stopped.

If your system does not resolve `*.localhost` names, add them to `/etc/hosts`:

```text
127.0.0.1 connect.localhost cbn.localhost nibss.localhost bank-a.localhost bank-b.localhost
```

The rest of the docs write `beerus` for `./beerus`.

## Start Beerus

```bash
./beerus up
```

```text
Beerus is running. Thu 19 Nov 2026 11:56 WAT.

No banks yet. Add the first one with beerus bank create --name NAME.

Press Ctrl+C to stop.
```

Starts CBN, NIBSS, Connect and core. `up` stays in the foreground and prints the addresses, the control room link and the participant logs.

Run every later command in a second terminal, in the same folder. Beerus keeps its state in `.beerus` in the current folder. If you use another state directory, set `BEERUS_DIR` to it in every terminal.

Output on these pages comes from a run with its ports moved to 18400 and up. A plain `beerus init` uses 7443 and up, so your ports differ.

## Add two banks

```bash
./beerus bank create --name "Bank A"
./beerus bank create --name "Bank B" --type digital
```

```text
Allocating codes (NIP 000001, NUBAN 001)                      ✓  0.0s
Registering with NIBSS                                        ✓  0.0s
Creating the bank database                                    ✓  0.0s
Migrating bank ledger (6 migrations)                          ✓  0.0s
Migrating open banking (4 migrations)                         ✓  0.0s
Issuing certificates                                          ✓  0.0s
Opening the CBN settlement account                            ✓  0.0s
Starting online banking                                       ✓  0.0s
Starting the open banking API                                 ✓  0.0s

Bank A is ready (1.8s) at http://bank-a.localhost:18409
```

Bank B prints the same steps with NIP code 000002. The bank ID comes from the name: `bank-a`, `bank-b`.

## Add customers and accounts

Customer IDs are random. Use the IDs your own run prints.

1. Add Ada at Bank A, with an online banking login.

   ```bash
   printf '%s' 'PASSWORD' | ./beerus customer create --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password-stdin --bank bank-a
   ```

   ```text
   Created customer cus_jmjyyf7yer62o4nemkhn, Ada Obi, at bank-a. BVN 22900000013.
   Ada Obi can sign in to online banking as ada.
   ```

   `PASSWORD` is the password Ada uses to sign in to Bank A's online banking.

2. Add Tunde at Bank B.

   ```bash
   ./beerus customer create --name "Tunde Bello" --phone 08037654321 --dob 1988-11-02 --bank bank-b
   ```

   ```text
   Created customer cus_oeoxqtrdrrjruhag4icv, Tunde Bello, at bank-b. BVN 22900000021.
   ```

3. Open a current account for Ada.

   ```bash
   ./beerus account open --customer cus_jmjyyf7yer62o4nemkhn --type current --bank bank-a
   ```

   ```text
   Opened current account 0000000014 for Ada Obi (cus_jmjyyf7yer62o4nemkhn) at bank-a. Balance ₦0.00.
   ```

4. Open a savings account for Tunde.

   ```bash
   ./beerus account open --customer cus_oeoxqtrdrrjruhag4icv --type savings --bank bank-b
   ```

   ```text
   Opened savings account 0000000011 for Tunde Bello (cus_oeoxqtrdrrjruhag4icv) at bank-b. Balance ₦0.00.
   ```

5. Deposit ₦50,000 in cash into Ada's account.

   ```bash
   ./beerus deposit 0000000014 --amount 50000 --bank bank-a
   ```

   ```text
   Deposited ₦50,000.00 into account 0000000014 at bank-a. Balance ₦50,000.00.
   ```

6. Withdraw ₦1,500.50 in cash.

   ```bash
   ./beerus withdraw 0000000014 --amount 1500.50 --bank bank-a
   ```

   ```text
   Withdrew ₦1,500.50 from account 0000000014 at bank-a. Balance ₦48,499.50.
   ```

## Send money

1. Check the charges without sending.

   ```bash
   ./beerus transfer --from 0000000014 --from-bank bank-a --to 0000000011 --to-bank bank-b --amount 10000 --dry-run
   ```

   ```text
   Sending ₦10,000.00 to Tunde Bello at Bank B.
   Charges: fee ₦10.00, VAT ₦0.75 and stamp duty ₦50.00. Total debit ₦10,060.75. Balance after ₦38,438.75.
   This was a quote. Nothing was sent.
   ```

2. Send it.

   ```bash
   ./beerus transfer --from 0000000014 --from-bank bank-a --to 0000000011 --to-bank bank-b --amount 10000 --narration 'Rent share'
   ```

   ```text
   Sending ₦10,000.00 to Tunde Bello at Bank B.
   Session 000001261119115733719221795118: success at Thu 19 Nov 2026 11:57 WAT
   ```

   Tunde's account is credited now. The banks settle with each other later.

## Settle

NIBSS settles at the next cutoff on its own. To settle now:

```bash
./beerus nip settle-now
```

```text
Settled 1 session at Thu 19 Nov 2026 11:57 WAT.
```

Check that the settlement balances:

```bash
./beerus nip settlement
```

```json
{
  "problems": [],
  "sessions": 1
}
```

## Stop Beerus

Press Ctrl+C in the terminal that runs `beerus up`. Beerus stops every participant. Your banks, customers and money stay in Postgres for the next `beerus up`.

Next: [how Beerus works](how-beerus-works.md)

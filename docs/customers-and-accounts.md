# Customers and accounts

Add customers to a bank, open their accounts and move cash in and out.

Needs: `beerus up` running and at least one bank. See [add and manage banks](banks.md#add-a-bank).

Every command here acts on one bank. Pass `--bank`, or pick a default bank first. Customer IDs are random, so use the IDs your run prints.

## Add a customer

```bash
beerus customer create --name "Tunde Bello" --phone 08037654321 --dob 1988-11-02 --bank bank-b
```

```text
Created customer cus_oeoxqtrdrrjruhag4icv, Tunde Bello, at bank-b. BVN 22900000021.
```

The bank gets a BVN for the customer from NIBSS. The phone number starts with `0`, `+234` or `234`. The date of birth is `YYYY-MM-DD`.

List a bank's customers:

```bash
beerus customers --bank bank-b
```

```text
ID                        NAME         PHONE          BVN          STATUS
cus_oeoxqtrdrrjruhag4icv  Tunde Bello  2348037654321  22900000021  active
1 customer on this page.
```

## Online banking login

A customer can sign in to online banking only with a username and password. Give both when you create the customer. The password comes from standard input:

```bash
printf '%s' 'PASSWORD' | beerus customer create --name "Ada Obi" --phone 08031234567 --dob 1990-05-14 --username ada --password-stdin --bank bank-a
```

```text
Created customer cus_jmjyyf7yer62o4nemkhn, Ada Obi, at bank-a. BVN 22900000013.
Ada Obi can sign in to online banking as ada.
```

`PASSWORD` is the customer's online banking password.

A username without a password fails:

```text
beerus: Username "ada" needs a password.
```

To sign in, open the bank's online banking address. `bank create` and `beerus info` print it, such as `http://bank-a.localhost:18409`. Your port differs.

1. Enter the username and password.
2. Read the 6-digit code from the page. The bank shows its text message there:

   ```text
   Messages · Bank A: Your Bank A verification code is 706261. It expires in 5 minutes. Never share this code with anyone.
   ```

3. Enter the code.

Five wrong passwords lock the login for 15 minutes. A session lasts 30 minutes. Customers need a login to connect an app; see [connect an app](open-banking.md).

## Open an account

```bash
beerus account open --customer cus_jmjyyf7yer62o4nemkhn --type current --bank bank-a
```

```text
Opened current account 0000000014 for Ada Obi (cus_jmjyyf7yer62o4nemkhn) at bank-a. Balance ₦0.00.
```

`--type` is `current` or `savings`. Anything else fails:

```text
beerus: Account type "fixed" must be current or savings.
```

The account number is a 10-digit NUBAN. Each bank numbers its accounts in order, so in a new setup the first account at bank-a is `0000000014`.

List a bank's accounts:

```bash
beerus accounts --bank bank-a
```

```text
NUBAN       OWNER      CUSTOMER                  TYPE     STATUS  BALANCE
0000000014  Ada Obi    cus_jmjyyf7yer62o4nemkhn  current  active  ₦0.00
0000000021  Chidi Eze  cus_icoerw7f6cnefmtsuv3g  savings  active  ₦0.00
2 accounts on this page.
```

Add `--customer ID` to list one customer's accounts. Lists take `--limit` (1 to 1000) and `--cursor`.

### Close an account or customer

An account must hold ₦0.00 before it closes. A customer must have no open accounts.

```bash
beerus account close 0000000021 --bank bank-a
beerus customer close cus_icoerw7f6cnefmtsuv3g --bank bank-a
```

```text
Closed account 0000000021 at bank-a.
Closed customer cus_icoerw7f6cnefmtsuv3g at bank-a.
```

Otherwise the close fails and says what is left:

```text
beerus: Account balance is not zero: account 0000000021 holds ₦5,000.00; move it out by transfer or cash first.
beerus: Customer has open accounts: customer cus_icoerw7f6cnefmtsuv3g still has 1 open account; close them first.
```

## Deposit and withdraw

Put the account number before the flags.

```bash
beerus deposit 0000000014 --amount 50000 --bank bank-a
beerus withdraw 0000000014 --amount 1500.50 --bank bank-a
```

```text
Deposited ₦50,000.00 into account 0000000014 at bank-a. Balance ₦50,000.00.
Withdrew ₦1,500.50 from account 0000000014 at bank-a. Balance ₦48,499.50.
```

Amounts are naira, with at most 2 decimals.

| Error | Cause |
| --- | --- |
| `Amount "12.345" must look like 25000 or 25000.50 (naira).` | More than 2 decimals |
| `"--amount" should be a value, not a flag; usage: beerus deposit <nuban> --amount NAIRA [options]` | Flags before the account number |
| `Account 0000000014 holds ₦48,499.50 and ₦60,000.00 was asked for; withdraw ₦48,499.50 or less.` | Not enough money |
| `Account 0000000014 has used ₦19,500.50 of its ₦5,000,000.00 daily limit and ₦5,500,000.00 was asked for; withdraw ₦4,980,499.50 or less today.` | Daily limit reached |

The daily limit counts cash withdrawals, transfers inside the bank and NIP sends, per Lagos day. Fees do not count. The ₦5,000,000.00 limit is a placeholder, not a CBN figure; see [placeholders](charges-and-limits.md#placeholders).

## Read a statement

```bash
beerus statement 0000000014 --bank bank-a
```

```text
DATE        TIME                       AMOUNT       BALANCE      NARRATION                                              SESSION
2026-11-19  Thu 19 Nov 2026 11:57 WAT  ₦50,000.00   ₦50,000.00   CASH DEPOSIT
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦1,500.50   ₦48,499.50   CASH WITHDRAWAL
2026-11-19  Thu 19 Nov 2026 11:57 WAT  ₦100,000.00  ₦148,499.50  CASH DEPOSIT
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦10,000.00  ₦138,499.50  NIP TRF TO Tunde Bello/Bank B/719221795118/Rent share  000001261119115733719221795118
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦10.00      ₦138,489.50  NIP CHARGE 719221795118                                000001261119115733719221795118
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦0.75       ₦138,488.75  VAT ON NIP CHARGE 719221795118                         000001261119115733719221795118
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦50.00      ₦138,438.75  STAMP DUTY 719221795118                                000001261119115733719221795118
2026-11-19  Thu 19 Nov 2026 11:57 WAT  −₦5,000.00   ₦133,438.75  TRF TO Chidi Eze/0000000021/Lunch
8 entries on this page.
```

Each charge is its own line, with the NIP session ID. Filter by date with `--from YYYY-MM-DD` and `--to YYYY-MM-DD`. Page with `--limit` and `--cursor`.

## Pick a default bank

```bash
beerus use bank-a
```

```text
Using Bank A. Type exit to leave.
```

`use` opens a sub-shell. Its prompt shows `(bank-a)`. Customer, account, cash and transfer commands in it act on Bank A. Type `exit` to leave.

`fault list` and `fault clear` ignore the default bank. A bare `fault clear` clears the faults of every bank. Inside the sub-shell, `use bank-b` switches bank:

```text
Switched to Bank B. Type exit to leave.
Fault: down until Thu 19 Nov 2026 12:09 WAT
```

The `Fault:` line appears only when the bank has a fault.

| Error | Cause |
| --- | --- |
| `bank "nope" is not in this setup; the banks are bank-a, bank-b, lapo-mfb` | Unknown bank ID |
| `No bank selected. Run beerus use <bank> or add --bank.` | No `--bank` and no `use` |

Next: [send transfers](transfers.md)

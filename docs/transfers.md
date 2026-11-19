# Send transfers

Move money between accounts, check a NIP session and settle it.

Needs: `beerus up` running and two funded accounts. The examples use Ada (`0000000014` at bank-a) and Tunde (`0000000011` at bank-b) from [get started](get-started.md).

## Check charges first

```bash
beerus transfer --from 0000000014 --from-bank bank-a --to 0000000011 --to-bank bank-b --amount 25000 --dry-run
```

```text
Sending ₦25,000.00 to Tunde Bello at Bank B.
Charges: fee ₦10.00, VAT ₦0.75 and stamp duty ₦50.00. Total debit ₦25,060.75. Balance after ₦123,438.75.
This was a quote. Nothing was sent.
```

`--dry-run` quotes the charges and sends nothing. Dry runs from the same run, by amount:

| Amount | Charges | Total debit |
| --- | --- | --- |
| ₦4,000.00 | none | ₦4,000.00 |
| ₦10,000.00 | fee ₦10.00, VAT ₦0.75, stamp duty ₦50.00 | ₦10,060.75 |
| ₦25,000.00 | fee ₦10.00, VAT ₦0.75, stamp duty ₦50.00 | ₦25,060.75 |
| ₦60,000.00 | fee ₦50.00, VAT ₦3.75, stamp duty ₦50.00 | ₦60,103.75 |

The bands and their sources are in [charges and limits](charges-and-limits.md#transfer-charges). A transfer inside one bank has no charges.

## Send a transfer

```bash
beerus transfer --from 0000000014 --from-bank bank-a --to 0000000011 --to-bank bank-b --amount 10000 --narration 'Rent share'
```

```text
Sending ₦10,000.00 to Tunde Bello at Bank B.
Session 000001261119115733719221795118: success at Thu 19 Nov 2026 11:57 WAT
Charges: fee ₦10.00, VAT ₦0.75 and stamp duty ₦50.00. Total debit ₦10,060.75. Balance ₦138,438.75.
```

The transfer goes over NIP. Tunde's bank credits him at once. The 30-digit session ID names the transfer.

`--from-bank` defaults to the bank chosen with `beerus use`. `--to-bank` defaults to the paying bank. Leave out `--to-bank` to move money inside one bank:

```bash
beerus transfer --from 0000000014 --from-bank bank-a --to 0000000021 --amount 5000 --narration 'Lunch'
```

```text
Moved ₦5,000.00 from 0000000014 to 0000000021, Chidi Eze, at Thu 19 Nov 2026 11:57 WAT. Balance ₦133,438.75.
```

Amounts are naira, with at most 2 decimals. Commas are allowed: `25,000.00`.

### Errors

| Message | Cause | Fix |
| --- | --- | --- |
| `Account 0000000014 does not have enough money for this transfer and its charges.` | Balance below amount plus charges | Deposit more, or send less |
| `That is not a valid account at Bank B, so nothing was debited.` | No such NUBAN at `--to-bank` | Check the number and bank |
| `The from and to accounts are the same.` | `--from` equals `--to` | Pick another account |
| `Bank B is not available right now, so nothing was debited.` | Receiving bank is down or closed | Clear the fault, or pick another bank |

## Check a session

```bash
beerus nip status 000001261119115733719221795118
```

```text
Session 000001261119115733719221795118: success at Thu 19 Nov 2026 11:57 WAT
Amount ₦10,000.00 from 0000000014 (bank code 000001) to 0000000011 (bank code 000002), started at Thu 19 Nov 2026 11:57 WAT.
```

`nip status` asks NIBSS now. A `transfer` to a bank that times out prints a pending session:

```text
Session 000001261119120007960861095662: pending. Beerus will check with NIBSS at Thu 19 Nov 2026 12:02 WAT. Run beerus nip status 000001261119120007960861095662.
No charges. Total debit ₦3,000.00. Balance ₦130,438.75.
```

Beerus checks a pending session on its own 2 minutes later. Run `nip status` to check it now:

```text
Session 000001261119120007960861095662: was pending; now success at Thu 19 Nov 2026 12:00 WAT
Amount ₦3,000.00 from 0000000014 (bank code 000001) to 0000000011 (bank code 000002), started at Thu 19 Nov 2026 12:00 WAT.
```

A pending transfer that failed resolves like this, and the sending bank puts the money back:

```text
Session 000001261119120025089515120565: failed with code 25. The sending bank reverses the debit.
The session was pending; this check resolved it.
```

An unknown ID fails with `No NIP session has that ID.` To make a bank time out on purpose, see [test failures](faults.md#make-transfers-time-out).

## Settle now

NIBSS settles open sessions at each cutoff on its own. To settle everything open now:

```bash
beerus nip settle-now
```

```text
Settled 1 session at Thu 19 Nov 2026 11:57 WAT.
```

With nothing open it prints `No sessions to settle at <time>.` `settle-now` works on any day, weekends and holidays included. The cutoff times are in [charges and limits](charges-and-limits.md#settlement-sessions).

Check that every settlement balances:

```bash
beerus nip settlement
```

```json
{
  "problems": [],
  "sessions": 1
}
```

## See positions

```bash
beerus nip positions
```

Excerpt for bank-a, after the ₦10,000 transfer and before settlement:

```json
{
  "bank_id": "bank-a",
  "cap_source": "default",
  "collateral_minor": null,
  "headroom_minor": 499999000000,
  "net_debit_cap_minor": 500000000000,
  "next_session": "2026-11-19T12:00:00+01:00",
  "nip_code": "000001",
  "open_inward_minor": 0,
  "open_outward_minor": 1000000,
  "position_minor": 1000000
}
```

Amounts are in kobo. `position_minor` is what the bank owes at the next session: outward minus inward. After settlement it returns to 0. NIBSS refuses a send that takes the position above `net_debit_cap_minor`. The default cap of ₦5,000,000,000.00 is a placeholder, not a NIBSS figure; see [placeholders](charges-and-limits.md#placeholders).

Next: [how Beerus works](how-beerus-works.md#a-transfer-in-two-halves)

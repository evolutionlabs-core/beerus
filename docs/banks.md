# Add and manage banks

Create, suspend, close and purge the banks in your setup.

Needs: Beerus running (`beerus up` in another terminal) and `BEERUS_DIR` set the same way in both terminals.

## Add a bank

```bash
beerus bank create --name "Bank A"
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

Beerus derives the bank ID from the name: lower case, other characters become `-`. `Bank A` becomes `bank-a`. Set your own with `--id`.

Beerus assigns the NIP code and NUBAN bank code. Codes go in order, first free code first.

Pick a type with `--type`. Commercial and digital banks share one code sequence.

| Type | NIP code | NUBAN code |
| --- | --- | --- |
| `commercial` (default) | `000001`, `000002`, ... | `001`, `002`, ... |
| `digital` | `000001`, `000002`, ... | `001`, `002`, ... |
| `microfinance` | `090001`, `090002`, ... | `50001`, `50002`, ... |

```bash
beerus bank create --name "Bank B" --type digital
```

```text
Allocating codes (NIP 000002, NUBAN 002)                      ✓  0.0s
...
Bank B is ready (2.0s) at http://bank-b.localhost:18410
```

Show one bank:

```bash
beerus describe bank bank-a
```

```text
Bank bank-a
  Phase       active
  Ready       yes
  Generation  1 (observed 1)
Spec
  id: bank-a
  name: Bank A
  type: commercial
  nip_code: "000001"
  nuban_code: "001"
Owned
  database beerus_c78692a9e458_bank_a
  port 18409
  port 18419
  dir banks/bank-a
```

`beerus get banks` lists every bank with its phase.

## Microfinance banks

```bash
beerus bank create --name "Lapo MFB" --type microfinance --settles-through bank-a
```

```text
Allocating codes (NIP 090001, NUBAN 50001)                      ✓  0.0s
...
Lapo MFB is ready (2.0s) at http://lapo-mfb.localhost:18411
```

`--settles-through` names a commercial or digital bank. If you leave it out, Beerus picks the first open commercial bank. It never picks a digital bank. With no commercial bank, the microfinance bank gets none.

Beerus records `settles_through` and uses it to order close and purge: close or purge the microfinance bank first. It does not change how the bank settles. You cannot change it after the bank exists.

## Suspend and resume

```bash
beerus bank suspend bank-b
```

```text
{
  "api_port": 18420,
  "database": "beerus_c78692a9e458_bank_b",
  "id": "bank-b",
  "name": "Bank B",
  "online_banking": "http://bank-b.localhost:18410",
  "phase": "suspended",
```

The output is the bank as JSON, trimmed here. A suspended bank keeps its data. Its online banking login answers 503 with `online banking is not available right now. Please try again later.` NIP transfers to it fail.

```bash
beerus bank resume bank-b
```

The bank returns to `"phase": "active"`. A closed bank cannot be suspended or resumed.

## Close a bank

Closing stops routing, settles open transfers, moves customer money, and keeps the database and history.

1. Choose what happens to customer money with `--money`:

   | `--money` | What happens |
   | --- | --- |
   | `transfer --to BANK` | Full balance moves over NIP to a new account at BANK |
   | `payout` | Cash paid out up to the NDIC cap per customer |
   | `leave` | Money stays on the account |

   Transfer moves carry no charges and do not count toward the daily limit. The target bank must be active, not suspended, down or faulted.

   Payout pays up to ₦5,000,000 per customer at a commercial or digital bank and ₦2,000,000 at a microfinance bank, across all that customer's accounts. The rest stays as an uninsured claim on a frozen account. Mapping digital banks to the ₦5,000,000 cap is a Beerus placeholder.

2. Close the bank. It asks you to type the bank ID. In a script, add `--yes` to skip that.

   ```bash
   beerus bank close bank-b --money transfer --to bank-a --yes --wait 30s
   ```

   ```text
   Setup beerus

     - bank bank-b      close   DESTRUCTIVE; on_remove +transfer to bank-a
           stop routing; settle 1 open transfer (net ₦3,000.00);
           transfer ₦15,000.00 for 1 customer to Bank A (on_remove); settle the moves. Its database and history stay.

   Plan: 1 to close.
   bank bank-b: SettlementPending: Waiting for the 18:00 NIP session to settle 1 transfer (net ₦3,000.00). Wait for it, or run beerus nip settle-now.
   beerus: bank bank-b: SettlementPending: Waiting for the 18:00 NIP session to settle 1 transfer (net ₦3,000.00). Wait for it, or run beerus nip settle-now.
   ```

   The command exits 3 when the wait ends blocked. The close keeps going in the background.

3. If the bank waits on a NIP session, settle now. The first run settles the open transfers. The second settles the money moves.

   ```bash
   beerus nip settle-now
   ```

   ```text
   Settled 1 session at Thu 19 Nov 2026 12:02 WAT.
   ```

   Run `beerus get banks` between runs. Repeat while the bank shows `closing` with `SettlementPending`.

4. Check the result.

   ```bash
   beerus describe bank bank-b
   ```

   ```text
   Bank bank-b
     Phase               closed
     Ready               yes
   ...
   Money of Bank B on Thu 19 Nov 2026 12:02 WAT:
     Tunde Bello  0000000011  ₦15,000.00  transferred to Bank A account 0000000038
   ```

Override the choice for one customer with `--customer ID=CHOICE`. Repeat it for more customers. `--to` is needed when any customer's money is transferred.

```bash
beerus bank close bank-b --money payout --customer cus_oeoxqtrdrrjruhag4icv=transfer --to bank-a --yes
```

A closed bank stays in the setup. Its online banking shows that it is closed. Transfers to it fail with `Bank B is not available right now, so nothing was debited.`

## Purge a bank

> [!WARNING]
> Purge deletes the bank's database and its folder in the state directory. It cannot be undone.

```bash
beerus bank purge bank-b --yes
```

```text
Setup beerus

  - bank bank-b      remove   DESTRUCTIVE
        stop routing;
        drop 1 database. Its ID and codes stay reserved.

Plan: 1 to remove.
bank bank-b: Retrying: purge-owned is not done yet; trying again (attempt 1)
Money of Bank B on Thu 19 Nov 2026 12:02 WAT:
  Tunde Bello  0000000011  ₦15,000.00  transferred to Bank A account 0000000038
```

The `Retrying` line is harmless. The purge finishes on the next attempt.

The bank ID and codes are never reused. `beerus describe bank bank-b` still prints `Bank bank-b was removed.` and the money table.

Purge works on an active bank too. It then needs the same `--money` decision as close. Purge a microfinance bank before the bank it settles through.

## Errors

| Message | Cause | Fix |
| --- | --- | --- |
| `usage: beerus bank create --name NAME [options]` | No `--name` | Add `--name` |
| `banks[bank-a-2].name "Bank A" is already used by bank-a. Give each bank its own name.` | Name taken | Use another name |
| `banks[z].type "credit" is not a bank type. Use commercial, digital or microfinance.` | Bad `--type` | Use a listed type |
| `banks[z].id "z" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.` | ID too short or bad | Use a longer name or `--id` |
| `bank-b needs a money decision. Add --money transfer, payout or leave` | No `--money` | Add `--money` |
| `--money transfer needs --to <bank>` | No target bank | Add `--to` |
| `--to is only for --money transfer. Payout is cash and needs no bank` | `--to` with payout | Drop `--to` |
| `no answer for bank-b. Nothing was changed` | No answer at `Type bank-b to confirm:` | Type the ID, or add `--yes` |
| `purging bank-b deletes its database and cannot be undone. Nothing was changed. Run again with --yes to purge it` | Purge from a script | Add `--yes` |
| `bank-b is already closed. Nothing was changed` | Close run again while closing | Run `beerus nip settle-now` |

Next: [customers and accounts](customers-and-accounts.md)

# Test failures

Make a bank unreachable or make its transfers time out, then clear the fault.

Needs: Beerus running, and two banks with funded accounts. The examples send from Ada Obi at bank-a (`0000000014`) to Tunde Bello at bank-b (`0000000011`).

## Take a bank down

1. Take bank-b down for 10 minutes.

```bash
beerus fault down --bank bank-b --for 10m
```

```text
Fault flt_4838a92e1b075ddae185 on bank-b: down until Thu 19 Nov 2026 12:09 WAT.
```

Without `--for`, the bank stays down until you clear it. The limit is 24 hours:

```text
beerus: for "48h" is above the limit of 24h
```

2. Send a transfer to it.

```bash
beerus transfer --from 0000000014 --to 0000000011 --to-bank bank-b --amount 1000
```

```text
beerus: Bank B is not available right now, so nothing was debited.
```

3. List the faults.

```bash
beerus fault list
```

```text
ID                        BANK    WHAT
flt_4838a92e1b075ddae185  bank-b  down until Thu 19 Nov 2026 12:09 WAT
```

Inside a `beerus use` shell, switching to bank-b shows the fault:

```text
Switched to Bank B. Type exit to leave.
Fault: down until Thu 19 Nov 2026 12:09 WAT
```

## Make transfers time out

A timeout fault on bank-b makes its next NIP calls time out. `--outcome` sets what really happened to the money:

| Outcome | Result after `nip status` |
| --- | --- |
| `credited` | Success. Tunde has the money. |
| `failed` | Failed with code 25. Bank A reverses the debit. |
| `unknown` | Pending until a status query gets an answer |

`--count` sets how many calls time out. The default is 1.

A `nip status` query counts as a call. With `--count 2`, one transfer uses the first and its first `nip status` uses the second.

### Credited

1. Add the fault.

```bash
beerus fault timeout --bank bank-b --outcome credited --count 1
```

```text
Fault flt_b809d33f735f47826b50 on bank-b: the next transfer times out (outcome credited).
```

2. Send a transfer. It stays pending and the fault is used up.

```bash
beerus transfer --from 0000000014 --to 0000000011 --to-bank bank-b --amount 3000 --narration 'Timeout test'
```

```text
Sending ₦3,000.00 to Tunde Bello at Bank B.
Session 000001261119120007960861095662: pending. Beerus will check with NIBSS at Thu 19 Nov 2026 12:02 WAT. Run beerus nip status 000001261119120007960861095662.
No charges. Total debit ₦3,000.00. Balance ₦130,438.75.
```

3. Ask NIBSS. The query resolves it now, before the time printed above.

```bash
beerus nip status 000001261119120007960861095662
```

```text
Session 000001261119120007960861095662: was pending; now success at Thu 19 Nov 2026 12:00 WAT
Amount ₦3,000.00 from 0000000014 (bank code 000001) to 0000000011 (bank code 000002), started at Thu 19 Nov 2026 12:00 WAT.
```

### Failed

1. Add a fault for 2 calls.

```bash
beerus fault timeout --bank bank-b --outcome failed --count 2
```

```text
Fault flt_1ed497bc240751e5fa69 on bank-b: the next 2 transfers time out (outcome failed).
```

2. Send a transfer. Ada's balance drops by ₦2,000.00 to ₦128,438.75.

```bash
beerus transfer --from 0000000014 --to 0000000011 --to-bank bank-b --amount 2000
```

```text
Sending ₦2,000.00 to Tunde Bello at Bank B.
Session 000001261119120025089515120565: pending. Beerus will check with NIBSS at Thu 19 Nov 2026 12:02 WAT. Run beerus nip status 000001261119120025089515120565.
No charges. Total debit ₦2,000.00. Balance ₦128,438.75.
```

3. Ask NIBSS. This query uses the second timeout, so the transfer stays pending.

```bash
beerus nip status 000001261119120025089515120565
```

```text
Session 000001261119120025089515120565: pending. Beerus will check with NIBSS at Thu 19 Nov 2026 12:02 WAT.
Amount ₦2,000.00 from 0000000014 (bank code 000001) to 0000000011 (bank code 000002), started at Thu 19 Nov 2026 12:00 WAT.
```

4. Ask again. The transfer fails and Bank A reverses the debit.

```bash
beerus nip status 000001261119120025089515120565
```

```text
Session 000001261119120025089515120565: failed with code 25. The sending bank reverses the debit.
The session was pending; this check resolved it.
Amount ₦2,000.00 from 0000000014 (bank code 000001) to 0000000011 (bank code 000002), started at Thu 19 Nov 2026 12:00 WAT.
```

Ada's statement shows the debit and its reversal:

```text
2026-11-19  Thu 19 Nov 2026 12:00 WAT  −₦2,000.00   ₦128,438.75  NIP TRF TO Tunde Bello/Bank B/089515120565               000001261119120025089515120565
2026-11-19  Thu 19 Nov 2026 12:00 WAT  ₦2,000.00    ₦130,438.75  REV/NIP TRF TO Tunde Bello/Bank B/089515120565           000001261119120025089515120565
```

A bad outcome fails:

```text
beerus: outcome "maybe" must be credited, failed or unknown
```

## Clear faults

Clear the faults of one bank:

```bash
beerus fault clear --bank bank-b
```

```text
Cleared 1 fault.
```

With nothing left to clear:

```text
No faults to clear.
```

`beerus fault clear` with no `--bank` clears the faults of every bank. It ignores the bank picked with `beerus use`. `fault list` with no `--bank` lists every bank. `fault down` and `fault timeout` do use the `beerus use` bank.

`fault list` drops a timeout fault once it is used up. `beerus bank purge` removes the purged bank's faults.

Next: [send transfers](transfers.md)

# Charges and limits

The fees, settlement times and limits Beerus applies, with the source of each. The rules live in `internal/countrypack/ng.yaml`.

## Transfer charges

Only NIP transfers between banks carry charges. A transfer inside one bank costs nothing. The sender pays.

| Amount | Fee |
| --- | --- |
| Up to ₦5,000.00 | ₦0.00 |
| ₦5,000.01 to ₦50,000.00 | ₦10.00 |
| Above ₦50,000.00 | ₦50.00 |

| Charge | Rule |
| --- | --- |
| VAT | 7.5% of the fee, rounded half to even to the kobo |
| Stamp duty | ₦50.00 on transfers of ₦10,000.00 and above |

Total debit = amount + fee + VAT + stamp duty.

| Charge | Source |
| --- | --- |
| Fee | [CBN Exposure Draft Guide to Charges 2026](https://www.cbn.gov.ng/Out/2026/CCD/Signed%20Guide%20to%20Charges%20April%202026.pdf), section 7.2.2. A draft; CBN has issued no final guide. |
| VAT | Finance Act 2019. The source line notes no page was opened to confirm it. |
| Stamp duty | Nigeria Tax Act 2025, as reported by [techeconomy.ng](https://techeconomy.ng/stamp-duty-replaces-emtl-sender-to-bear-50-charge-on-bank-transfers) |

Quotes from `beerus transfer --dry-run`:

```text
Sending ₦4,000.00 to Tunde Bello at Bank B.
No charges. Total debit ₦4,000.00. Balance after ₦44,499.50.
```

```text
Sending ₦10,000.00 to Tunde Bello at Bank B.
Charges: fee ₦10.00, VAT ₦0.75 and stamp duty ₦50.00. Total debit ₦10,060.75. Balance after ₦38,438.75.
```

```text
Sending ₦60,000.00 to Tunde Bello at Bank B.
Charges: fee ₦50.00, VAT ₦3.75 and stamp duty ₦50.00. Total debit ₦60,103.75. Balance after ₦88,395.75.
```

Money moved when a bank closes carries no charges.

## Settlement sessions

NIBSS settles the banks' net positions at CBN in sessions. The default is 4 sessions a day, at these Lagos times:

| Session | Cutoff |
| --- | --- |
| 1 | 06:00 |
| 2 | 12:00 |
| 3 | 18:00 |
| 4 | 24:00 |

- Sessions run on business days only: Monday to Friday, except holidays.
- A transfer made at a weekend or on a holiday waits for the next business-day cutoff.
- `nip.sessions` in the setup file sets the count. Cutoffs are spread evenly from 00:00. The count must split the day into whole minutes.
- `nip.sessions` cannot change after the first settlement.
- `beerus nip settle-now` settles everything open now, on any day.

```bash
beerus nip settle-now
```

```text
Settled 1 session at Thu 19 Nov 2026 11:57 WAT.
```

Holidays are four fixed dates, from the [Public Holidays Act](https://placng.org/lawsofnigeria/view2.php?sn=467). Beerus does not move them off weekends and has no Easter or Eid. Add others with `holidays.add` in the setup file.

| Date | Holiday |
| --- | --- |
| 1 January | New Year's Day |
| 1 May | Workers' Day |
| 1 October | Independence Day |
| 25 December | Christmas Day |

## Limits

### Daily limit

Each account has a daily limit per Lagos day. It counts cash withdrawals, own-bank transfers and NIP sends. It does not count fees or a reversed transfer. The limit depends on the KYC tier. Every account is tier 3 by default. The figures are placeholders.

```text
beerus: Account 0000000014 has used ₦19,500.50 of its ₦5,000,000.00 daily limit and ₦5,500,000.00 was asked for; withdraw ₦4,980,499.50 or less today.
```

### Net debit cap

NIBSS refuses a send that would take the sending bank's net debit position past its cap, with code 61. The default caps are placeholders. Set `net_debit_cap_minor` and `collateral_minor` per bank in the setup file. `beerus nip positions` shows each bank's cap and `cap_source`.

### Deposit insurance

When a bank closes with `--money payout`, each customer gets cash up to the NDIC cover, across all their accounts at that bank. The rest stays as an uninsured claim on a frozen account.

| Bank type | Cover per customer |
| --- | --- |
| Commercial | ₦5,000,000.00 |
| Digital | ₦5,000,000.00 |
| Microfinance | ₦2,000,000.00 |

Source: NDIC raised the cover on 24-25 April 2024, as reported by [guardian.ng](https://guardian.ng/business-services/ndic-raises-maximum-bank-deposit-insurance-coverage-to-n5m/). Giving digital banks the deposit money bank cover is a placeholder. Many real digital banks hold microfinance licences.

## Placeholders

Beerus invents these figures. None is a CBN or NIBSS value.

| Figure | Value |
| --- | --- |
| Net debit cap, commercial and digital | ₦5,000,000,000.00 |
| Net debit cap, microfinance | ₦500,000,000.00 |
| Daily limit, tier 1 | ₦50,000.00 |
| Daily limit, tier 2 | ₦200,000.00 |
| Daily limit, tier 3 | ₦5,000,000.00 |
| History kept, commercial | 730 days |
| History kept, digital | 180 days |
| History kept, microfinance | 365 days |
| CBN settlement account opening balance, per bank | ₦10,000,000,000.00 |
| CBN vault cash opening balance, per bank | ₦2,000,000,000.00 |
| NDIC cover for digital banks | Deposit money bank cover |

History kept limits how far back `start_date` reaches for an open banking connection.

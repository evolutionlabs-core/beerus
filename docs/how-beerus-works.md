# How Beerus works

Beerus runs the CBN, NIBSS, banks and an open banking aggregator as separate processes on your machine. Money moves between them the way it moves in Nigeria: instant credit over NIP, then net settlement at the CBN.

Banks start empty. Money and data change only when you, your app or the clock change them. Beerus runs on real Lagos time (WAT, +01:00) and cannot move or backdate the clock.

## Who does what

| Participant | Real world | In Beerus |
| --- | --- | --- |
| CBN | Central bank, runs RTGS | Holds each bank's settlement account; root of trust |
| NIBSS | Interbank switch | NIP, BVNs, settlement sessions, open banking registry, consent ledger |
| Banks | Hold customer accounts | Own database, ledger, online banking, open banking API |
| Connect | Open banking aggregator | Partner API and link widget for your app |
| core | No real-world part | Starts participants, adds banks, serves the control API |

Each participant reads only its own Postgres database. Participants call each other over mTLS. The CBN is the root of trust for their certificates. `beerus up` starts CBN, core, NIBSS and Connect, then core starts one process per bank.

## A transfer in two halves

In Nigeria, an NIP transfer credits the receiver within seconds. The banks pay each other later, in net, through their settlement accounts at the CBN. Beerus does the same.

The first half happens when you send. For ₦10,000 from Ada at Bank A to Tunde at Bank B:

1. Bank A debits Ada's account and credits its NIP outward payable.
2. NIBSS checks Bank A's net debit cap and forwards the transfer to Bank B.
3. Bank B debits its NIP inward receivable and credits Tunde's account.

Tunde can spend the money now. No money has moved between the banks yet. NIBSS records the transfer in an open session. `beerus nip positions` shows Bank A's open outward amount as `1000000` kobo.

If Bank B does not answer, the transfer stays pending. Beerus checks with NIBSS after 2 minutes, or at once when you run `beerus nip status`. If the credit failed, Bank A reverses the debit.

The second half is settlement. NIBSS nets every open transfer per bank, and the CBN moves the nets between settlement accounts. Each bank then clears its outward payable and inward receivable against its CBN settlement account. Bank A's position returns to 0.

## Settlement

NIBSS settles in sessions. The default is 4 a day, with cutoffs at 06:00, 12:00, 18:00 and 24:00 Lagos time. The setup file sets the number of sessions; see [setup file reference](setup-reference.md).

Sessions run on business days only: Monday to Friday, except holidays. A transfer sent on a Saturday waits for Monday's first cutoff. The built-in holidays are 1 January, 1 May, 1 October and 25 December. Add others in the setup file.

`beerus nip settle-now` runs an extra session at once, on any day. It settles everything open. Use it to see settlement without waiting for a cutoff.

The nets of one session sum to zero. The CBN refuses a posting that does not balance. `beerus nip settlement` checks every session.

Each new bank gets a CBN settlement account of ₦10,000,000,000.00 and vault cash of ₦2,000,000,000.00. Both are placeholders, as is the net debit cap. The full list is in [charges and limits](charges-and-limits.md#placeholders).

## Charges

Charges apply to NIP transfers only. A transfer inside one bank costs nothing. The sending bank takes the charges from the sender when the transfer is sent:

| Statement line | Bank account credited |
| --- | --- |
| `NIP CHARGE` | Fee income |
| `VAT ON NIP CHARGE` | VAT payable |
| `STAMP DUTY` | Stamp duty payable |

The charges stay at the sending bank. NIBSS settles the transfer amount only, which is why Bank A's position is ₦10,000.00, not the ₦10,060.75 total debit. VAT is 7.5% of the fee. Stamp duty is ₦50.00 on transfers of ₦10,000.00 and above.

Beerus keeps only the current charge rules, with no fee history.

## Open banking

The CBN Operational Guidelines for Open Banking (March 2023) require FAPI 2.0, mTLS, certificate-bound tokens, time-bound consent and a central registry. Beerus enforces these in every environment, including local development. Interbank messages follow the structure of ISO 20022.

Connect is an aggregator listed in the NIBSS registry. Your app is Connect's partner:

1. Your server creates a link session at Connect's partner API.
2. The customer opens the link, picks a bank and signs in there with a password and a 6-digit code.
3. The customer picks the accounts to share and approves. The bank records the consent in the NIBSS consent ledger.
4. Connect sends the customer back to your app with a code. Your server exchanges it for a connection.
5. Connect reads the accounts and transactions from the bank and tells your app by webhook.

Consent lasts 12 months by default. The customer can revoke it at the bank, and your app can delete the connection. The steps and commands are in [connect an app](open-banking.md).

Next: [send transfers](transfers.md)

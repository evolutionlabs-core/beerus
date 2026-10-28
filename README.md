# Beerus

Beerus is a full simulation of the Nigerian banking system that runs on your own infrastructure. It lets developers send transfers, post charges, settle between banks and share account data through open banking, with each step behaving the way it does in production.

## What it models

- **Banks** with double-entry core ledgers, internet banking with 2FA, charges, VAT and stamp duty, reversals and end-of-day processing.
- **NIBSS**: the NIP switch with name enquiry, transfers and status queries, deferred net settlement in sessions, the BVN registry, the Open Banking Registry and the consent ledger.
- **CBN**: the trust anchor for every participant's certificates, and the RTGS that holds banks' settlement accounts.
- **Beerus Connect**: an open banking aggregator. Applications connect through its partner API to get account data and a transaction feed.
- **Simulator**: a seeded population of customers whose activity produces repeatable transaction history.

Each institution is a separate service with its own database. They communicate only over mTLS with signed, idempotent messages, as they would in production.

## Standards

Open banking follows the CBN Operational Guidelines for Open Banking (March 2023): FAPI 2.0, mTLS, certificate-bound tokens, time-bound consent and a central registry. Interbank messages follow the structure of ISO 20022. These controls are enforced in every environment, including local development.

## License

Beerus is distributed under the terms in [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

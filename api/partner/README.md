# Beerus Connect API

The Partner API your app calls to link a customer's bank and read their data.

**Base URL:** `http://connect.localhost:7443`

## Contents

- [Authentication](#authentication)
- [Flow](#flow)
- [Endpoints](#endpoints)
- [Webhooks](#webhooks)
- [Errors](#errors)
- [Conventions](#conventions)

## Authentication

Every request carries your app's credentials. Calls about a connection also carry its access token.

| Header | Value | Sent on |
| --- | --- | --- |
| `Beerus-Client-ID` | Your app's ID | Every request |
| `Beerus-Secret` | Your app's secret | Every request |
| `Beerus-Access-Token` | The connection's access token | Requests marked ★ |
| `Idempotency-Key` | A unique key per write | Every `POST` |

```bash
curl http://connect.localhost:7443/v1/accounts \
  -H "Beerus-Client-ID: myapp" \
  -H "Beerus-Secret: YOUR_SECRET" \
  -H "Beerus-Access-Token: ACCESS_TOKEN"
```

## Flow

1. `POST /v1/link/token/create` returns a `link_token`.
2. Connect opens with the `link_token`. The customer links their bank and you get a `public_token`.
3. `POST /v1/public_token/exchange` swaps the `public_token` for an `access_token`.
4. Read the customer's data with the `access_token`.

## Endpoints

### Link

| Endpoint | Description |
| --- | --- |
| `POST /v1/link/token/create` | Create a link token |
| `GET /v1/link_sessions/{link_session_id}` | Get a link session |
| `GET /v1/institutions` | List the banks a customer can link |
| `POST /v1/public_token/exchange` | Exchange a public token for an access token |

### Connection

| Endpoint | Description |
| --- | --- |
| `GET /v1/connection` ★ | Get the connection |
| `POST /v1/connection/reauth` ★ | Ask the customer to link the bank again |
| `POST /v1/connections/remove` ★ | Remove the connection |

### Accounts

| Endpoint | Description |
| --- | --- |
| `GET /v1/accounts` ★ | List the connection's accounts |
| `GET /v1/accounts/{account_id}` ★ | Get an account |
| `GET /v1/accounts/{account_id}/balance` ★ | Get the balance at the end of a day |
| `GET /v1/accounts/{account_id}/transactions/sync` ★ | Get new and changed transactions |
| `POST /v1/accounts/{account_id}/refresh` ★ | Ask for fresh data |

### Webhook keys

| Endpoint | Description |
| --- | --- |
| `GET /v1/webhook_keys` | List the public keys that sign webhooks |

## Webhooks

Connect sends these to the webhook URL you registered. They only notify: fetch the data through the API.

| Event | Sent when |
| --- | --- |
| `sync.available` | New data is ready |
| `historical_sync.completed` | History is loaded |
| `connection.status_changed` | A connection's status changed |
| `account.added` | An account was added |
| `account.removed` | An account was removed |
| `refresh.completed` | A refresh finished |

Webhooks follow [Standard Webhooks](https://www.standardwebhooks.com) and are signed with ed25519. Verify `webhook-signature` with a key from `GET /v1/webhook_keys`, and answer with any `2xx` within 15 seconds.

## Errors

Errors share one shape. Branch on `code`, never on `message`.

```json
{"error": {"code": "invalid_access_token", "message": "This access_token is not valid.", "request_id": "req_jc6e66ye8g0etjfm"}}
```

| Code | Status | Meaning |
| --- | --- | --- |
| `invalid_request` | 400, 422 | The request is malformed, or a value must change |
| `invalid_cursor` | 400 | The cursor cannot be read |
| `unauthenticated` | 401 | The client ID or secret is wrong |
| `invalid_access_token` | 401 | The access token is unknown or removed |
| `forbidden` | 403 | Your app may not do this |
| `not_found` | 404 | The resource does not exist |
| `invalid_public_token` | 404 | The public token is unknown, used or expired |
| `cursor_reset` | 409 | Beerus was reset; start the sync again |
| `connection_revoked` | 409 | The connection was revoked |
| `reauth_required` | 409 | The customer must link the bank again |
| `link_session_expired` | 409 | The link session expired |
| `idempotency_key_reused` | 422 | The key was used with a different body |
| `as_of_out_of_range` | 422 | `as_of` is outside the dates Connect holds |
| `rate_limited` | 429 | Too many requests |
| `institution_down` | 503 | The bank cannot be reached |
| `internal_error` | 500 | Something went wrong in Connect |

## Conventions

| | |
| --- | --- |
| Amounts | Integers in kobo, with a currency code |
| Dates | `YYYY-MM-DD`, in Lagos time |
| Lists | Paged with `limit` and `cursor` |
| Writes | Repeating a `POST` with the same `Idempotency-Key` returns the same response |

The full contract, with every field, is in [openapi.yaml](openapi.yaml).

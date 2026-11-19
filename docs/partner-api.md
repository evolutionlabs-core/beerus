# Partner API reference

The HTTP API a partner app calls on Beerus Connect. The spec is `api/partner/openapi.yaml`.

Base URL: `https://connect.localhost:7443/v1` by default. `beerus info` prints the address of a running setup. Times are Lagos time with the offset. Money is `amount_minor` in kobo, with `currency` `NGN`.

## Authentication

Every route needs both:

- An mTLS client certificate issued by Connect for the partner.
- `X-Api-Key: bk_test_...` belonging to the same partner.

`beerus partner add <id>` writes them to `.beerus/partners/<id>/`:

| File | Use |
| --- | --- |
| `client.crt` | Client certificate |
| `client.key` | Key for the client certificate |
| `ca.crt` | CA for the server certificate |
| `api-key` | `X-Api-Key` value |

```bash
curl --cert .beerus/partners/balance/client.crt --key .beerus/partners/balance/client.key --cacert .beerus/partners/balance/ca.crt -H "X-Api-Key: $API_KEY" https://connect.localhost:7443/v1/webhook_keys
```

| Problem | Status | Code | Message |
| --- | --- | --- | --- |
| No client certificate | none | none | TLS handshake fails |
| Certificate of no registered partner | 403 | `forbidden` | `The client certificate does not belong to a registered partner.` |
| No `X-Api-Key` | 401 | `unauthenticated` | `The X-Api-Key header is required.` |
| Unknown key | 401 | `unauthenticated` | `The API key is not valid.` |
| Another partner's key | 403 | `forbidden` | `The API key does not belong to the client certificate's partner.` |
| Over 500 requests a second, burst 1,000 | 429 | `rate_limited` | `Too many requests.` with `Retry-After: 1` |

`beerus partner rotate-key <id>` issues a new key and certificate. The old ones stop working.

## Idempotency

Every write needs `Idempotency-Key`, 1 to 255 characters.

| Rule | Value |
| --- | --- |
| Routes | Every `POST` and `DELETE` |
| Scope | Partner, method, path and key |
| Kept for | 24 hours |
| Same key, same body | Original response, byte for byte, with `Idempotent-Replayed: true` |
| Same key, other body | 422 `idempotency_key_reused` |
| No key | 400 `invalid_request` |
| Body over 1 MiB | 413 `invalid_request` |

```text
HTTP/1.1 400 Bad Request

{"error":{"code":"invalid_request","message":"An Idempotency-Key header of 1 to 255 characters is required on writes.","request_id":"req_0e4h3yngek4snt6b"}}
```

## Routes

| Method | Path | What |
| --- | --- | --- |
| `POST` | `/v1/link_sessions` | Create a link session. 201. |
| `GET` | `/v1/link_sessions/{id}` | Link session status and `connection_id` |
| `GET` | `/v1/webhook_keys` | Webhook public key |
| `POST` | `/v1/connections/exchange` | Swap a code for a connection. 201 new, 200 reauthorised. |
| `GET` | `/v1/connections/{id}` | One connection |
| `DELETE` | `/v1/connections/{id}` | Revoke the consent at the bank. 204. |
| `GET` | `/v1/connections/{id}/accounts` | Shared accounts with balances |
| `POST` | `/v1/connections/{id}/reauth` | New link session for the same connection. 201. |
| `GET` | `/v1/accounts/{id}` | One account with balances |
| `GET` | `/v1/accounts/{id}/balance?as_of=YYYY-MM-DD` | Closing ledger balance of a past day |
| `GET` | `/v1/accounts/{id}/transactions/sync` | Changes since a cursor |
| `POST` | `/v1/accounts/{id}/refresh` | Fetch from the bank now. 202. |

### Link sessions

`POST /v1/link_sessions` body:

| Field | Rule |
| --- | --- |
| `start_date` | Required. `YYYY-MM-DD`, not in the future |
| `workspace_id` | Required. 1 to 128 characters |
| `redirect_uri` | Required. Exact match of a registered URI |
| `state` | Optional. Up to 512 characters, returned unchanged |

The response holds `link_token` and `widget_url`. The link token lasts 30 minutes, the session 60 minutes.

Status values: `created`, `bank_chosen`, `at_bank`, `authorised`, `exchanged`, `expired`.

The redirect to `redirect_uri` carries `code`, `link_session_id` and `state`. On failure it carries `error` in place of `code`: `access_denied`, `link_session_expired`, `institution_down` or `server_error`.

### Exchange

Body: `{"code":"oc_...","link_session_id":"lks_..."}`, both required. A code lasts 10 minutes and works once.

Connection status values: `active`, `syncing`, `reauth_required`, `revoked`, `expired`, `institution_down`, `error`. Consent lasts 12 months by default.

### Reauth

Optional body: `{"redirect_uri":"...","state":"..."}`. `redirect_uri` defaults to the partner's first one. The exchange that follows returns 200 with the same connection and account IDs.

### Balance

`as_of` must fall between the day before the first date Connect holds and the last completed business day.

### Transactions sync

| Query | Default | Meaning |
| --- | --- | --- |
| `cursor` | none | `next_cursor` of the last call. None starts from the beginning. |
| `limit` | 100 | 1 to 500 |

The response has `added`, `modified`, `removed`, `next_cursor`, `has_more` and `world_id`. `amount_minor` is negative for money out. `removed` lists pending lines only, as `{id}`.

### Refresh

The response is `{"id":"ref_...","account_id":...,"status":"queued","requested_at":...}`. Each account takes one refresh a minute by default, set by `connect.refresh_min_interval`. More gets 429 `rate_limited` with `Retry-After`. The job ends with `refresh.completed`, then `sync.available` if data changed.

## Errors

Every error has this body:

```json
{"error":{"code":"invalid_code","message":"The code was already used.","request_id":"req_953ww2539wzjk1km"}}
```

Every response carries a `Request-Id` header. On errors it matches `request_id` in the body.

| Status | Code |
| --- | --- |
| 400 | `invalid_request`, `invalid_cursor` |
| 401 | `unauthenticated` |
| 403 | `forbidden`, `link_session_mismatch` |
| 404 | `not_found`, `invalid_code` |
| 409 | `cursor_reset`, `connection_revoked`, `reauth_required`, `account_not_shared`, `link_session_expired` |
| 413 | `invalid_request` |
| 422 | `idempotency_key_reused`, `as_of_out_of_range` |
| 429 | `rate_limited` |
| 500 | `internal_error` |
| 502 | `bank_error` |
| 503 | `not_ready`, `institution_down` |

`not_ready` means Connect is still registering with NIBSS after `beerus up`. Its message is `Connect is still registering with NIBSS. Try again in a minute.`

`cursor_reset` means the data was replaced after a reset or an unclean shutdown. Sync again with no cursor.

## Webhooks

Connect posts JSON to the partner's webhook URL. The format follows Standard Webhooks with ed25519 signatures.

| Header | Value |
| --- | --- |
| `webhook-id` | Delivery ID, `msg_...`. Same on every retry. |
| `webhook-timestamp` | Unix seconds of this attempt |
| `webhook-signature` | Space-separated `v1a,<base64 signature>` |
| `User-Agent` | `Beerus-Connect-Webhooks/1` |
| `Content-Type` | `application/json` |

The signed message is `<webhook-id>.<webhook-timestamp>.<raw body>`.

The public key is `whpk_` plus the standard base64 of the raw 32-byte ed25519 key. One key is active. `GET /v1/webhook_keys`, `beerus info` and `beerus partner add` print it.

The body is `{"type","timestamp","data"}`. `timestamp` is when the event happened.

| Type | `data` |
| --- | --- |
| `account.added` | `connection_id`, `account_id` |
| `account.removed` | `connection_id`, `account_id` |
| `historical_sync.completed` | `connection_id`, `account_id`, `start_date`, `effective_start_date` |
| `sync.available` | `connection_id`, `account_id`, `world_id` |
| `connection.status_changed` | `connection_id`, `previous_status`, `status`, `error_code` |
| `refresh.completed` | `refresh_id`, `connection_id`, `account_id`, `outcome`, `error_code` |

Delivery is at least once, in no fixed order.

A delivery succeeds on any 2xx within 15 seconds. Otherwise Connect retries. There are 12 attempts in all, each delay ±10%:

| Attempt | Delay after the previous attempt |
| --- | --- |
| 1 | none |
| 2 | 5 s |
| 3 | 30 s |
| 4 | 2 min |
| 5 | 10 min |
| 6 | 30 min |
| 7 | 2 h |
| 8 | 5 h |
| 9 | 10 h |
| 10 | 14 h |
| 11 | 20 h |
| 12 | 20 h |

After the 12th attempt the status is `failed`. A partner with no webhook URL gets status `no_endpoint`.

Delivery status values: `pending`, `sending`, `delivered`, `failed`, `no_endpoint`.

`beerus webhooks list` shows deliveries. `beerus webhooks replay <id>` resets the attempts and sends again with the same `webhook-id`.

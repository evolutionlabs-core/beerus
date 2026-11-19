# Connect an app

Connect a partner app to a customer's bank account through Beerus Connect, then read the account.

Needs: Beerus running, a bank, and a customer with an account, created with `--username` and `--password-stdin`. See [online banking login](customers-and-accounts.md#online-banking-login).

The examples use the ports from one run. `beerus info` prints yours.

## Register your app

1. Register the app with its redirect URI and webhook URL.

```bash
beerus partner add balance --redirect-uri http://localhost:3000/bank_connections/callback --webhook-url http://127.0.0.1:18431/webhooks
```

```text
Registered balance.

API key             bk_test_<masked>  (shown once; also in .beerus/partners/balance/api-key)
Partner API         https://connect.localhost:18400
Client certificate  .beerus/partners/balance/client.crt
Client key          .beerus/partners/balance/client.key
Server CA           .beerus/partners/balance/ca.crt
Redirect URIs       http://localhost:3000/bank_connections/callback
Webhook URL         http://127.0.0.1:18431/webhooks
Webhook public key  whpk_VT6JUH1WAvwq8+2KeXzLStaqD1mjp/cnHjzgrWtB/ow=
```

The credentials land in `.beerus/partners/balance/`:

| File | Use |
| --- | --- |
| `client.crt` | mTLS client certificate |
| `client.key` | Key for the client certificate |
| `ca.crt` | CA that signs the partner API's certificate |
| `api-key` | Value of the `X-Api-Key` header |

2. Set the paths and key for the calls below.

```bash
P=.beerus/partners/balance
API_KEY=$(cat $P/api-key)
```

3. Check the credentials work.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" https://connect.localhost:18400/v1/webhook_keys
```

```json
{"data":[{"key_id":"whk_1YnTmFM9a8w","public_key":"whpk_VT6JUH1WAvwq8+2KeXzLStaqD1mjp/cnHjzgrWtB/ow=","status":"active"}]}
```

If the client certificate is missing, the TLS handshake fails and curl exits 56. There is no JSON error.

## Create a link session

Your server creates a link session for one customer.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" -X POST https://connect.localhost:18400/v1/link_sessions -d '{"start_date":"2026-11-01","workspace_id":"ws_adaeze_bakery","redirect_uri":"http://localhost:3000/bank_connections/callback","state":"9f86d081884c7d65"}'
```

```json
{"connection_id":null,"created_at":"2026-11-19T11:58:06+01:00","id":"lks_ecs0wzr3qtdvraf1","link_token":"lt_zvzxcss040fpp2rnyz8dxh015tp1t04e","link_token_expires_at":"2026-11-19T12:28:06+01:00","start_date":"2026-11-01","status":"created","widget_url":"http://connect.localhost:18401/link?token=lt_zvzxcss040fpp2rnyz8dxh015tp1t04e","workspace_id":"ws_adaeze_bakery"}
```

| Field | Rule |
| --- | --- |
| `start_date` | Required. `YYYY-MM-DD`, not in the future. History starts here. |
| `workspace_id` | Required. Your ID for the customer, 1 to 128 characters. |
| `redirect_uri` | Required. Must exactly match a registered redirect URI. |
| `state` | Optional. Up to 512 characters, returned unchanged. |

Lifetimes, in real time:

- Link token: 30 minutes.
- Link session: 60 minutes.
- Code from the redirect: 10 minutes, and it works once.

## Customer consents

The customer does these steps in a browser.

1. Open the `widget_url`. The widget root, `http://connect.localhost:18401/`, has no page. The widget opens only from a link session's `widget_url`.
2. Pick a bank on the **Connect your bank** page.
3. Sign in at the bank with the online banking username and password.
4. Enter the 6-digit code. The bank site shows it on the page. It expires in 5 minutes.
5. Tick the accounts to share, then click **Approve**.

The browser lands on your `redirect_uri`:

```text
http://localhost:3000/bank_connections/callback?code=oc_nng8w9dme2fr6sa1wfq5k34xvr&link_session_id=lks_ecs0wzr3qtdvraf1&state=9f86d081884c7d65
```

Check that `state` matches the value you sent.

If the customer declines or cancels, or the flow fails, the redirect carries `error` in place of `code`: `access_denied`, `link_session_expired`, `institution_down` or `server_error`.

The link session now shows `authorised`:

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" https://connect.localhost:18400/v1/link_sessions/lks_ecs0wzr3qtdvraf1
```

```json
{"connection_id":null,"created_at":"2026-11-19T11:58:06+01:00","id":"lks_ecs0wzr3qtdvraf1","start_date":"2026-11-01","status":"authorised","workspace_id":"ws_adaeze_bakery"}
```

## Exchange the code

Swap the code for a connection.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" -H "Idempotency-Key: $(uuidgen)" -H "Content-Type: application/json" -X POST https://connect.localhost:18400/v1/connections/exchange -d '{"code":"oc_nng8w9dme2fr6sa1wfq5k34xvr","link_session_id":"lks_ecs0wzr3qtdvraf1"}'
```

```json
{"consent_expires_at":"2027-11-19T11:58:51+01:00","created_at":"2026-11-19T11:59:10+01:00","effective_start_date":null,"id":"con_tb9egky6ey7v163w","institution":{"id":"bank-a","name":"Bank A"},"last_synced_at":null,"start_date":"2026-11-01","status":"syncing","workspace_id":"ws_adaeze_bakery"}
```

Keep the connection ID. The status moves from `syncing` to `active` once Connect has read the accounts.

A retry with the same `Idempotency-Key` and body returns the same response with `Idempotent-Replayed: true`. The same code with a new key fails:

```text
HTTP/1.1 404 Not Found

{"error":{"code":"invalid_code","message":"The code was already used.","request_id":"req_953ww2539wzjk1km"}}
```

If you lose the exchange response, `GET /v1/link_sessions/{id}` returns the `connection_id`.

## Read accounts and transactions

1. List the connection's accounts.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" https://connect.localhost:18400/v1/connections/con_tb9egky6ey7v163w/accounts
```

```json
{"data":[{"account_number":"0000000014","balances":{"as_at":"2026-11-19T11:59:11+01:00","available":{"amount_minor":13343875,"currency":"NGN"},"current":{"amount_minor":13343875,"currency":"NGN"}},"connection_id":"con_tb9egky6ey7v163w","currency":"NGN","id":"acc_h5dunkvo6m4jcjcith3o","institution":{"id":"bank-a","name":"Bank A"},"name":"Ada Obi","type":"current"}]}
```

Amounts are in kobo. `13343875` is ₦133,438.75.

2. Read the closing balance of a past day.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" "https://connect.localhost:18400/v1/accounts/acc_h5dunkvo6m4jcjcith3o/balance?as_of=2026-11-18"
```

```json
{"account_id":"acc_h5dunkvo6m4jcjcith3o","as_of":"2026-11-18","effective_start_date":null,"ledger_balance":{"amount_minor":0,"currency":"NGN"}}
```

The balance is 0 because the account was funded on 2026-11-19. `as_of` must fall between the day before `start_date` and the last completed business day. Today fails:

```json
{"error":{"code":"as_of_out_of_range","message":"as_of must be between 2026-10-31 and 2026-11-18, the day before the first date Connect holds and the last completed business day.","request_id":"req_gfgnry63153j8bsy"}}
```

3. Sync transactions. Start with no cursor.

```bash
curl --cert $P/client.crt --key $P/client.key --cacert $P/ca.crt -H "X-Api-Key: $API_KEY" "https://connect.localhost:18400/v1/accounts/acc_h5dunkvo6m4jcjcith3o/transactions/sync?limit=3"
```

<details>

<summary>Response</summary>

```json
{"added":[{"id":"txn_mxlyn6pii7irydqhitde","account_id":"acc_h5dunkvo6m4jcjcith3o","sequence":1,"amount_minor":5000000,"currency":"NGN","status":"posted","pending_transaction_id":null,"booked_at":"2026-11-19T11:57:20+01:00","value_date":"2026-11-19","description_raw":"CASH DEPOSIT","description":"CASH DEPOSIT","counterparty":null,"references":{"session_id":null,"bank_reference":"0010000000002"},"channel":"branch","balance_after_minor":5000000},{"id":"txn_2xpanu57jupvluvlc2lq","account_id":"acc_h5dunkvo6m4jcjcith3o","sequence":2,"amount_minor":-150050,"currency":"NGN","status":"posted","pending_transaction_id":null,"booked_at":"2026-11-19T11:57:20+01:00","value_date":"2026-11-19","description_raw":"CASH WITHDRAWAL","description":"CASH WITHDRAWAL","counterparty":null,"references":{"session_id":null,"bank_reference":"0010000000003"},"channel":"branch","balance_after_minor":4849950},{"id":"txn_o5yc6bmoygug2runkkwu","account_id":"acc_h5dunkvo6m4jcjcith3o","sequence":3,"amount_minor":10000000,"currency":"NGN","status":"posted","pending_transaction_id":null,"booked_at":"2026-11-19T11:57:33+01:00","value_date":"2026-11-19","description_raw":"CASH DEPOSIT","description":"CASH DEPOSIT","counterparty":null,"references":{"session_id":null,"bank_reference":"0010000000004"},"channel":"branch","balance_after_minor":14849950}],"has_more":true,"modified":[],"next_cursor":"cur_eyJ3Ijoid2xkX3pyOHQzOHJjeXM1czhoZzQiLCJhIjoiYWNjX2g1ZHVua3ZvNm00amNqY2l0aDNvIiwicyI6M30","removed":[],"world_id":"wld_zr8t38rcys5s8hg4"}
```

</details>

4. Call again with `cursor=<next_cursor>` while `has_more` is `true`. Upsert lines by `id` and delete the IDs in `removed`.

If a call returns `409 cursor_reset`, start again with no cursor.

Connect reads the banks every minute by default. Read now from the CLI:

```bash
beerus sync
```

```json
{
  "status": "synced"
}
```

`beerus connections` lists every connection with its customer and accounts.

## Receive webhooks

Connect posts events to your webhook URL. One delivery, as received:

```text
POST /webhooks
User-Agent: Beerus-Connect-Webhooks/1
Content-Type: application/json
Webhook-Id: msg_86vk2b141zhqv7km
Webhook-Signature: v1a,<signature>
Webhook-Timestamp: 1795085950

{"type":"account.added","timestamp":"2026-11-19T11:59:10+01:00","data":{"account_id":"acc_h5dunkvo6m4jcjcith3o","connection_id":"con_tb9egky6ey7v163w"}}
```

Verify each delivery:

1. Take the public key from `partner add`, `beerus info` or `GET /v1/webhook_keys`. It is `whpk_` plus the standard base64 of the raw 32-byte ed25519 key.
2. Reject a `webhook-timestamp` more than 5 minutes from now.
3. Verify a `v1a` signature over `<webhook-id>.<webhook-timestamp>.<raw body>`.
4. Answer 2xx within 15 seconds. Skip a `webhook-id` you have already handled.

```go
func verify(pub ed25519.PublicKey, id, ts string, body []byte, header string) bool {
	msg := append([]byte(id+"."+ts+"."), body...)
	for _, s := range strings.Fields(header) {
		b64, ok := strings.CutPrefix(s, "v1a,")
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && ed25519.Verify(pub, msg, sig) {
			return true
		}
	}
	return false
}
```

The flow above sent `account.added`, `historical_sync.completed`, `sync.available` and `connection.status_changed`. Webhooks only notify. Read the data through the API.

List deliveries and send one again:

```bash
beerus webhooks list
beerus webhooks replay msg_ekga1az4egat9zfx
```

```json
{
  "status": "pending"
}
```

A replay keeps the `webhook-id` and carries a new timestamp. The retry schedule is in the partner API reference.

## Revoke a connection

```bash
beerus revoke con_tb9egky6ey7v163w
```

```json
{
  "status": "revoked"
}
```

Beerus revokes the consent at the bank and sends a webhook:

```json
{"type":"connection.status_changed","timestamp":"2026-11-19T11:59:40+01:00","data":{"connection_id":"con_tb9egky6ey7v163w","error_code":"connection_revoked","previous_status":"active","status":"revoked"}}
```

`GET /v1/connections/con_tb9egky6ey7v163w` now returns `"status":"revoked"`. Reads on its accounts return `409 connection_revoked`.

From your app, send `DELETE /v1/connections/{id}` with an `Idempotency-Key`. It returns 204.

Next: [partner API reference](partner-api.md)

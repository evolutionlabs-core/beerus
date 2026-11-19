# Setup file reference

Every field of the setup file (`<dir>/setup.yaml` or any `-f FILE`) and of `<dir>/local.yaml`.

## Parsing

- One YAML document per file. Unknown fields are errors.
- Errors read `file:line:col: what. How to fix.` Beerus prints every error, not only the first.
- Money fields end in `_minor` and are whole kobo: `150` is ₦1.50.
- Durations use Go syntax: `30s`, `2m`, `1h`.

| Error | Cause |
| --- | --- |
| `unknown field "start". Remove it.` | Field does not exist |
| `unknown field "sesions" (did you mean "sessions"?)` | Typo |
| `<file>: the file is empty. Write the settings in it.` | Empty file |
| `the file holds more than one document. Keep one setup per file.` | Two `---` documents |
| `... is not a whole number. Write digits only, such as 4.` | Text in a number field |
| `... is not a whole number. Give the amount in kobo, such as 150 for 1.50 naira.` | Bad `_minor` value |

## Top level

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `apiVersion` | string | required | `beerus.dev/v1` |
| `kind` | string | required | `Setup` |
| `metadata.name` | string | required | 2 to 31 of `a-z 0-9 -`, starts with a letter |
| `spec` | mapping | | `nip`, `holidays`, `connect`, `banks`, `partners` |

`beerus init` names the setup `beerus`. `beerus reset <name>` must use this name. A file whose name differs from the state directory's setup fails with `metadata.name is X, but this dir holds setup Y. Use the file for Y, or another dir.`

## spec.nip

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `sessions` | int | `4` | Settlement sessions per day; must divide 1440 |
| `tsq_after` | duration | `2m` | Wait before the first status query on a timed-out transfer; above zero |

Sessions are evenly spaced from 00:00 Lagos time. `4` gives cutoffs at 06:00, 12:00, 18:00 and 24:00. Sessions run on business days only.

Errors:

- `nip.sessions 5 does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24.`
- `nip.tsq_after "soon" is not a duration. Use a duration such as 2m.`
- `nip.tsq_after 0s must be longer than zero. Use a duration such as 2m.`

`sessions` cannot change after `beerus init`. Plan error: `nip.sessions cannot change from 4 to 6. Put back 4.` NIBSS also refuses: `NIBSS has settled with 4 sessions a day, so nip_sessions must stay 4`. Run `beerus reset` to start again with another value.

## spec.holidays

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `add` | list of `YYYY-MM-DD` | empty | Extra non-business days |
| `remove` | list of `YYYY-MM-DD` | empty | Built-in holidays to treat as business days |

Built-in holidays: 1 January, 1 May, 1 October, 25 December. Weekends are never business days.

Errors:

- `holidays.add "x" is not a date. Use YYYY-MM-DD.`
- `holidays.add 2026-12-28 appears twice. Keep one.`
- `holidays.remove 2026-12-28 is also in holidays.add. Keep it in one list.`
- `holidays.add 2026-11-19 is today and cannot change.` (also for past dates: `is in the past and cannot change.`)

## spec.connect

Names Connect shows on the consent screen.

| Field | Type | Default |
| --- | --- | --- |
| `legal_name` | string | `Beerus Connect Limited` |
| `brand_name` | string | `Beerus Connect` |
| `registry_id` | string | `OBR-T2-000001` (placeholder) |
| `cac_number` | string | `RC 0000001` (placeholder) |

## spec.banks

A list. Default empty.

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `id` | string | required | 2 to 31 of `a-z 0-9 -`, starts with a letter |
| `name` | string | required | 1 to 40 characters, unique ignoring case |
| `type` | string | required | `commercial`, `digital` or `microfinance` |
| `settles_through` | bank ID | none | Commercial or digital bank in the file |
| `state` | string | `active` | `active`, `suspended` or `closed` |
| `net_debit_cap_minor` | int64 kobo | by type | Above zero |
| `collateral_minor` | int64 kobo | none | Above zero |
| `open_banking` | mapping | | See below |
| `on_remove` | mapping | none | See below |

- `id` cannot be `cbn`, `nibss` or `connect`. A removed bank's ID is never reused.
- `type` and `settles_through` cannot change after the bank exists.
- A new bank cannot start `closed`. A closed bank stays closed.
- `state: closed` needs `on_remove`.
- Default `net_debit_cap_minor`: ₦5,000,000,000.00 for commercial and digital, ₦500,000,000.00 for microfinance. Invented figures, not NIBSS values.
- `collateral_minor` shows in `beerus nip positions`. No rule uses it.

Errors:

- `banks[x].id "x" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`
- `banks[cbn].id cbn is reserved for the CBN. Choose another id.`
- `banks[bank-a-2].name "Bank A" is already used by bank-a. Give each bank its own name.`
- `banks[x].type "credit" is not a bank type. Use commercial, digital or microfinance.`
- `banks[bank-a].type cannot change from commercial to digital. Put back commercial.`
- `banks[x].settles_through lapo-mfb is a microfinance bank. Name a commercial or digital bank.`
- `banks[x].state cannot be closed for a new bank. Use active or suspended.`
- `banks[x].state closed needs on_remove. Add on_remove with money: transfer, payout or leave.`
- `banks[x].state cannot change from closed to active. A closed bank stays closed; remove it from the file instead.`
- `banks[x] is being removed, and its id is never reused. Choose another id.`

### nip_code and nuban_code

Beerus assigns both. Leave them out of your file.

- For a new bank: `banks[x].nip_code is assigned by Beerus. Remove it.`
- For an existing bank, a different value: `banks[bank-a].nip_code cannot change; it is 000001`

After an apply, `<dir>/setup.yaml` holds the assigned codes. `beerus export` leaves them out.

### open_banking

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `consent_months` | int | `12` | Consent length; above zero |
| `refresh_days` | int | `372` | Refresh token life; above zero |
| `dormant_days` | int | `180` | Above zero |
| `assertion_seconds` | int | `300` | `private_key_jwt` assertion life; above zero |
| `access_seconds` | int | `600` | Access token life; 130 to 3600 |

### on_remove

Used when the bank has `state: closed`, or when `apply --prune` removes it.

| Field | Type | Rule |
| --- | --- | --- |
| `money` | string | Required: `transfer`, `payout` or `leave` |
| `to` | bank ID | Required when any money is transferred; another bank in the file |
| `customers` | map of customer ID to choice | Per-customer `transfer`, `payout` or `leave` |

Payout pays up to the NDIC cap per customer: ₦5,000,000 for commercial and digital banks, ₦2,000,000 for microfinance. Digital banks use the ₦5,000,000 cap as a Beerus placeholder.

Errors:

- `banks[x].on_remove.money is required. Use transfer, payout or leave.`
- `banks[x].on_remove.to is required when money is transferred. Name the bank that receives it.`
- `banks[x].on_remove.to is set but no money is transferred. Payout is cash and needs no bank. Remove to, or choose transfer.`
- `banks[x].on_remove.to cannot name the bank being removed. Name another bank.`

## spec.partners

A list. Default empty.

| Field | Type | Default | Rule |
| --- | --- | --- | --- |
| `id` | string | required | 1 to 40 of `a-z 0-9 -`, starts with a letter or digit |
| `name` | string | required | Shown on the consent screen |
| `legal_name` | string | `name` | Shown on the consent screen |
| `purpose` | string | `to read your account details, balances and transactions` | Shown on the consent screen |
| `redirect_uris` | list of URLs | empty | http or https |
| `webhook_url` | URL | none | http or https |

The file accepts a partner with no redirect URI. `beerus partner add` needs at least one.

Error: `partners[balance].webhook_url "x" is not an http or https URL. Use a full URL such as https://localhost:3000/callback.`

## Full example

Not run as written. Every field follows the rules above.

```yaml
apiVersion: beerus.dev/v1
kind: Setup
metadata:
  name: beerus
spec:
  nip:
    sessions: 4
    tsq_after: 2m
  holidays:
    add:
      - 2026-12-28
    remove:
      - 2027-10-01
  connect:
    legal_name: Beerus Connect Limited
    brand_name: Beerus Connect
  banks:
    - id: bank-a
      name: Bank A
      type: commercial
      net_debit_cap_minor: 500000000000
      collateral_minor: 100000000000
      open_banking:
        consent_months: 12
        access_seconds: 600
    - id: bank-b
      name: Bank B
      type: digital
      state: suspended
    - id: lapo-mfb
      name: Lapo MFB
      type: microfinance
      settles_through: bank-a
      on_remove:
        money: transfer
        to: bank-a
  partners:
    - id: balance
      name: Balance
      legal_name: Balance Test Limited
      purpose: to keep your books in sync with this account
      redirect_uris:
        - http://localhost:3000/bank_connections/callback
      webhook_url: http://localhost:3000/webhooks/beerus
```

## local.yaml

Machine-local settings in `<dir>/local.yaml`. `beerus init` writes it once. `beerus reset` keeps it.

| Field | Default | Meaning |
| --- | --- | --- |
| `database_url` | `postgres:///postgres` | Postgres URL; `BEERUS_DATABASE_URL` overrides it |
| `bind` | | Checked as an IP address; `beerus up` does not use it |
| `ports.api_offset` | `10` | Bank API port is the first free port from site port + offset |
| `ports.window` | `200` | Ports above `partner_port` that banks may use |
| `connect.host` | `connect.localhost` | Connect host name |
| `connect.partner_port` | `7443` | Partner API; banks take the next free ports |
| `connect.widget_port` | `7444` | Connect widget |
| `connect.widget_tls` | `false` | `true` serves the widget over https |
| `connect.cbn_port` | `7446` | CBN |
| `connect.control_port` | `7480` | Control API and control room |
| `connect.nibss_port` | `7481` | NIBSS |
| `connect.connect_control_port` | first free from `7482` | Connect control |
| `connect.health_port` | `7490` | Health port of the cbn container |
| `connect.sync_interval` | `1m` | How often Connect syncs connections |
| `connect.refresh_min_interval` | `1m` | Least time between refreshes of one account |
| `partners.<id>.redirect_uris` | | Overrides the setup's redirect URIs on this machine |
| `partners.<id>.webhook_url` | | Overrides the setup's webhook URL on this machine |

Errors:

- `connect.partner_port 0 is not a port. Use a number from 1 to 65535, or leave it out.`
- `connect.sync_interval "x" is not a duration longer than zero. Use a duration such as 30s.`
- `The port window of 200 is full with N bank(s). Raise ports.window in local.yaml.`

Next: [describe a setup in a file](setup-file.md)

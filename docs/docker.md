# Run with Docker Compose

Run CBN, NIBSS, Connect, core and each bank in its own container.

Needs: Docker with Compose, a `beerus` binary, and a Postgres the host can reach for `beerus init`. Run the commands from the repository root: the compose file builds the image from its `Dockerfile`.

These steps follow the `multi-process` job in `.github/workflows/ci.yml`.

## Steps

1. Write the setup file into a new state folder.

   ```bash
   mkdir -p beerus-state
   ```

   Save this as `beerus-state/setup.yaml`:

   ```yaml
   apiVersion: beerus.dev/v1
   kind: Setup
   metadata:
     name: beerus
   spec:
     nip:
       sessions: 4
       tsq_after: 2m
     banks:
       - id: bank-a
         name: Bank A
         type: commercial
       - id: bank-b
         name: Bank B
         type: digital
   ```

2. Render a state folder from it. `init` checks Postgres and creates the CBN and NIBSS databases there. The URL below is CI's; use your own.

   ```bash
   BEERUS_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable beerus init --dir render -f beerus-state/setup.yaml
   ```

3. Print the compose file. It has one service for each bank in the setup.

   ```bash
   beerus compose --dir render > compose.ci.yaml
   ```

   `beerus reset beerus --dir render` later drops the databases `init` made.

   Keep the repository's `compose.yaml` as it is. It is the template for a setup with no banks.

4. If the setup lists no partners, set the partner app for the cbn container.

   ```bash
   export BEERUS_PARTNER_ID=balance
   export BEERUS_PARTNER_NAME=Balance
   export BEERUS_PARTNER_LEGAL_NAME="Balance Test Limited"
   export BEERUS_PARTNER_PURPOSE="to keep your books in sync with this account"
   export BEERUS_PARTNER_REDIRECT_URI=https://balance.localhost/bank_connections/callback
   ```

5. Point Compose at the rendered file and the state folder. Export these in every terminal you use for `docker compose`.

   ```bash
   export COMPOSE_FILE=compose.ci.yaml
   export BEERUS_STATE="$PWD/beerus-state"
   export BEERUS_UID="$(id -u)"
   export BEERUS_GID="$(id -g)"
   export BEERUS_SETUP_FILE=/state/setup.yaml
   ```

6. Start the stack.

   ```bash
   docker compose up -d --build --wait --wait-timeout 300
   ```

   `--wait` returns when every health check passes.

## Variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `COMPOSE_FILE` | `compose.yaml` | Compose file Docker reads |
| `BEERUS_STATE` | volume `state` | Host folder mounted at `/state` |
| `BEERUS_UID` | `10001` | User the containers run as |
| `BEERUS_GID` | `10001` | Group the containers run as |
| `BEERUS_SETUP_FILE` | empty | Setup file inside the container |
| `BEERUS_PARTNER_ID` | required with no partners | Partner app ID |
| `BEERUS_PARTNER_NAME` | required with no partners | App name on the consent screen |
| `BEERUS_PARTNER_LEGAL_NAME` | required with no partners | Legal name on the consent screen |
| `BEERUS_PARTNER_PURPOSE` | required with no partners | Purpose on the consent screen |
| `BEERUS_PARTNER_REDIRECT_URI` | required with no partners | Partner callback URL |
| `BEERUS_PARTNER_WEBHOOK_URL` | empty | Partner webhook URL |

- The cbn container reads `BEERUS_SETUP_FILE` and the partner variables once, when it first makes the state.
- The state folder must be writable by `BEERUS_UID` and `BEERUS_GID`.
- Each container's `BEERUS_DIR` is `/state`.

## Ports

Compose publishes these on `127.0.0.1`, with the default ports from `init`:

| Port | Service | What |
| --- | --- | --- |
| `7443` | core | Partner API |
| `7444` | core | Connect widget |
| `7480` | core | Control API |
| `7446` | cbn | CBN |
| `7490` | cbn | Health |
| bank port | each bank | Online banking |

Bank open banking API ports are not published. Containers reach each other as `cbn.localhost`, `connect.localhost` and `<bank-id>.localhost`.

On Linux, add the `*.localhost` names to `/etc/hosts`. See [reset and clean up](housekeeping.md#resolve-localhost-on-linux).

## Logs and stop

```bash
docker compose logs --no-color
```

```bash
docker compose down -v
```

`-v` also deletes the volumes, including the Postgres data.

## Errors

| Message | Cause | Fix |
| --- | --- | --- |
| `set the partner app's ID` | Setup has no partners and `BEERUS_PARTNER_ID` is unset | Set the five partner variables |
| `BEERUS_PARTNER_REDIRECT_URI needs BEERUS_PARTNER_ID, BEERUS_PARTNER_NAME, BEERUS_PARTNER_LEGAL_NAME and BEERUS_PARTNER_PURPOSE` | Partner variables incomplete | Set all five |
| `BEERUS_SETUP_FILE: <reason>` | Setup file missing or invalid; cbn stops | Fix the file |

Next: [describe a setup in a file](setup-file.md)

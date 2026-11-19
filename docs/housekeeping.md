# Reset and clean up

Find leftover databases, start a setup again, and set up your machine for Beerus.

## Find leftovers

```bash
beerus doctor
```

```text
5 databases belong to .beerus.
Nothing is left behind.
```

When something is left behind, `doctor` lists it under these headings:

- `Leftover databases:` databases with this state directory's prefix that no bank owns.
- `Status entries for banks that no longer exist:` banks the status file still tracks.
- `Missing databases (beerus up recreates them):` databases the setup expects but Postgres lacks.

It then prints `Run beerus doctor --fix to clean this up.`

Stop `beerus up` first, then fix:

```bash
beerus doctor --fix
```

`--fix` drops the leftover databases and forgets the status entries. It prints `Fixed.` Missing databases need no fix.

## Reset a setup

> [!WARNING]
> Reset drops every database of this setup and clears the state directory. It cannot be undone.

1. Stop `beerus up` with Ctrl+C.
2. Reset, naming the setup. The default setup is `beerus`.

   ```bash
   beerus reset beerus
   ```

   ```text
   Dropped databases:
     beerus_c78692a9e458_bank_a
     beerus_c78692a9e458_bank_c
     beerus_c78692a9e458_cbn
     beerus_c78692a9e458_lapo_mfb
     beerus_c78692a9e458_nibss
   Cleared .beerus (kept local.yaml). Run beerus init to start again.
   ```

3. Start again.

   ```bash
   beerus init
   ```

Reset keeps `local.yaml`, so your ports and database URL stay.

| Message | Cause | Fix |
| --- | --- | --- |
| `the name does not match this setup: .beerus holds setup "beerus", not "wrong-name"` | Wrong setup name | Use the name it shows |
| `reset .beerus: Beerus is running; stop it first, or delete the running file if it is not` | `beerus up` still running | Stop it with Ctrl+C |
| `.beerus holds no Beerus setup; run beerus init to make one` | Empty state directory | Run `beerus init` |

## Trust the certificate

Trust the Beerus root certificate so browsers accept the `https://*.localhost` addresses.

```bash
beerus trust
```

- macOS: adds `<dir>/ca/root.crt` to your login keychain. It may ask for your password.
- Linux: copies it to `/usr/local/share/ca-certificates/beerus-root.crt` with `sudo` and runs `update-ca-certificates`.
- Other systems: add `<dir>/ca/root.crt` to your trust store by hand.

The certificate is limited to `localhost` and `*.localhost`. Restart your browser after `trust`. Firefox keeps its own store: import the certificate there.

Remove it:

```bash
beerus untrust
```

## Use another state directory

Beerus keeps its state in `.beerus` under the current folder. Set `BEERUS_DIR` to use another one:

```bash
export BEERUS_DIR="$HOME/beerus-state"
```

Set it the same way in every terminal. Only some commands take `--dir`; the rest read `BEERUS_DIR` alone. See [commands](commands.md).

## Change ports

| Port | Default | `init` flag |
| --- | --- | --- |
| Partner API | `7443` | `--partner-port` |
| Connect widget | `7444` | `--widget-port` |
| CBN | `7446` | `--cbn-port` |
| Control API | `7480` | `--control-port` |
| NIBSS | `7481` | `--nibss-port` |
| Connect control | first free from `7482` | `--connect-control-port` |
| Health (cbn container) | `7490` | `--health-port` |

Banks take the next free ports above the partner port. A bank's open banking API takes the first free port 10 above its site port.

Set ports when you run `init`:

```bash
beerus init --partner-port 18400 --widget-port 18401 --control-port 18402 --health-port 18403 --nibss-port 18404 --cbn-port 18405 --connect-control-port 18406
```

After `init`, edit the `connect.*_port` fields in `<dir>/local.yaml` and restart `beerus up`. The fields are in the [setup file reference](setup-reference.md#localyaml).

## Resolve *.localhost on Linux

macOS resolves `*.localhost` names. On Linux, add them to `/etc/hosts`. CI adds this line:

```bash
echo "127.0.0.1 connect.localhost cbn.localhost bank-a.localhost bank-b.localhost" | sudo tee -a /etc/hosts
```

Add `<bank-id>.localhost` for each bank you create.

Next: [run with Docker Compose](docker.md)

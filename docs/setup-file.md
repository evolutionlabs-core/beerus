# Describe a setup in a file

Keep banks, partners and NIP settings in one YAML file and apply it to Beerus.

Needs: a finished `beerus init`. `apply` also needs `beerus up` running.

## Export the running setup

```bash
beerus export
```

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
    - id: lapo-mfb
      name: Lapo MFB
      type: microfinance
      settles_through: bank-a
  partners:
    - id: balance
      name: balance
      redirect_uris:
        - http://localhost:3000/bank_connections/callback
      webhook_url: http://127.0.0.1:18431/webhooks
```

The export leaves out `nip_code` and `nuban_code`, so the file works in a fresh setup.

Write it to a file:

```bash
beerus export -o my-setup.yaml
```

```text
Wrote my-setup.yaml.
```

The file has mode 0600. `-o` and `--json` cannot be used together.

Print a hash of the applied setup, to compare two runs:

```bash
beerus verify
```

```text
setup sha256:99ea9d7e0bb4cb609c86c0055e43bf38e0779b381f7d10f58e148b71f9957490
```

## Preview and apply

1. Edit the file. This adds a bank:

   ```yaml
       - id: bank-c
         name: Bank C
         type: commercial
   ```

2. Preview the change.

   ```bash
   beerus diff -f my-setup.yaml
   ```

   ```text
   Setup beerus

     + bank bank-c      create   commercial, NIP 000003 (allocated), NUBAN 003 (allocated)

   Plan: 1 to create.
   ```

   `diff` exits 2 when there are changes and 0 with `No changes.` It works with Beerus stopped.

3. Apply it.

   ```bash
   beerus apply -f my-setup.yaml
   ```

   ```text
   Setup beerus

     + bank bank-c      create   commercial, NIP 000003 (allocated), NUBAN 003 (allocated)

   Plan: 1 to create.
   ```

4. Check it.

   ```bash
   beerus diff -f my-setup.yaml
   ```

   ```text
   No changes.
   ```

Without `-f`, `diff` and `apply` read `setup.yaml` in the state directory. Each apply rewrites that file to the applied setup, with the assigned codes.

A bank or partner missing from the file shows as drift. `apply` leaves it in place. To remove it, add `--prune`.

> [!WARNING]
> `--prune` purges every bank missing from the file and deletes its database. It does not ask first.

```bash
beerus apply -f my-setup.yaml --prune --money transfer --to bank-a
```

Removing a bank that holds money needs `--money`, as in [close a bank](banks.md#close-a-bank). `--settle-now` runs an extra NIP session for banks that are closing or being removed. `--wait` sets how long to wait (default `5m0s`); the command exits 3 if the wait ends blocked.

To close a bank from the file, set `state: closed` and `on_remove` on it.

## Start from a file

Set up a new state directory from the file, then start it:

```bash
beerus init -f my-setup.yaml
beerus up
```

Or apply a file when Beerus starts:

```bash
beerus up -f my-setup.yaml
```

`up -f` checks the file before it starts anything. Add `--prune` to remove banks and partners that are not in the file.

## Errors

Beerus checks the whole file and prints every problem as `file:line:col: what. How to fix.`

```bash
beerus diff -f bad-typo.yaml
```

```text
beerus: bad-typo.yaml:7:5: unknown field "sesions" (did you mean "sessions"?)
bad-typo.yaml:8:5: nip.tsq_after "soon" is not a duration. Use a duration such as 2m.
bad-typo.yaml:10:7: banks[x].id "x" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.
bad-typo.yaml:12:7: banks[x].type "credit" is not a bank type. Use commercial, digital or microfinance.
```

| Message | Cause | Fix |
| --- | --- | --- |
| `unknown field "start". Remove it.` | `spec.start` no longer exists | Delete the line |
| `empty.yaml: the file is empty. Write the settings in it.` | Empty file | Write the setup |
| `read nope.yaml: open nope.yaml: no such file or directory` | Wrong path | Fix the path |
| `--json and -o cannot be used together` | Both on `export` | Use one |

Next: [setup file reference](setup-reference.md)

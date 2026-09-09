# Security

What lmm protects, how, and — just as importantly — what it does not
protect. lmm is a single-user tool that runs as you, on your machine, so
most of this is about what happens to your data when it *leaves* that
machine.

## Credentials at rest

`lmm auth login <source>` stores a source's API key in the local SQLite
database (`$XDG_DATA_HOME/lmm/lmm.db`, default
`~/.local/share/lmm/lmm.db`). Since v2.0.0 that key is **encrypted**
(issue #79); before it, the raw key sat in the database as readable text.

### How

- **Cipher:** AES-256-GCM, from the Go standard library. No third-party
  crypto dependency, in keeping with lmm's minimal-dependency rule.
- **Envelope:** each row stores `"lmm1"` + a 12-byte nonce + the
  ciphertext. The `lmm1` prefix is a version marker, and lmm tells an
  encrypted row from a plaintext one from an older lmm by the prefix *and*
  the shape — an envelope is at least 32 bytes and its body is random
  binary, where a credential you pasted is printable text. A plaintext key
  that happens to start with `lmm1` is therefore still recognised as
  plaintext and re-encrypted.
- **Nonce:** fresh `crypto/rand` bytes for every write, never reused.
- **Associated data:** the source id. A ciphertext copied from one row into
  another fails to decrypt rather than quietly authenticating the wrong
  source.
- **Key:** 32 `crypto/rand` bytes in `$XDG_DATA_HOME/lmm/key` (default
  `~/.local/share/lmm/key`), created the first time lmm has a credential to
  protect — a user who never logs in never has one. It is written `0600`
  inside a `0700` data directory, with `O_EXCL` so an existing key is never
  overwritten.

### Migration

Opening a database that still holds plaintext credentials re-encrypts every
one of them, in a single transaction, before anything reads the table. The
step is idempotent (a row already carrying `lmm1` is left alone) and runs on
every open, so a database restored from an old backup is fixed the next time
lmm touches it.

Afterwards lmm rebuilds the database file (`VACUUM`) and checkpoints and
truncates the write-ahead log, so the old plaintext is gone from
`lmm.db-wal` as well as `lmm.db`. Both steps need the database to itself:
another lmm process holding it open — `lmm serve` running while you use the
CLI, or a second shell — blocks them, and neither reports that as an error
of its own. Each is retried, and if the plaintext still cannot be removed
**the open fails** naming the file and telling you to close the other
process. That is deliberate: succeeding here would tell you your
credentials are encrypted while a backup or a file sync could still copy
them in the clear.

### Threat model — read this part

Encryption at rest protects a database that **leaves your machine**:

- a `~/.local/share/lmm` folder swept up by a backup tool or a cloud sync
- a database copied to another machine, or attached to a bug report
- a stolen disk or a restored snapshot

It does **not** protect against a local attacker running as you. The key
file sits next to the database and is readable by your own user, because
lmm has to be able to decrypt without prompting — that is what makes
non-interactive and scripted use possible. Anything that can read
`lmm.db` can read `key`.

Two consequences worth stating plainly:

- **The key file is the secret.** Back it up *with* the database, or
  neither is any use. If you copy `lmm.db` to a new machine without `key`,
  your stored credentials cannot be recovered — run
  `lmm auth login <source>` again for each one.
- **If you want protection from a local attacker**, an OS keyring or a
  passphrase would be needed. Both were considered and rejected for v2: a
  keyring means a D-Bus/Secret Service dependency plus a headless fallback
  path in what is otherwise a single static binary, and a passphrase
  prompt on every credential read breaks non-interactive use.

### What a status surface shows

`lmm auth status`, `GET /api/v1/auth`, and the web UI's Setup page never
decrypt a stored credential. They report presence, when it was stored and
last replaced, and a **fingerprint** — the first 8 hex digits of the key's
SHA-256 — which is enough to answer "is this still the key I pasted?" and
tells an attacker nothing. A key supplied through an environment variable
is a different case: lmm holds it in the clear regardless, so that row
still shows the familiar masked `abc...xyz` form alongside its fingerprint.

Only the sources themselves get the key in the clear, at the moment they
put it on a request.

### When something goes wrong

| Symptom                                                        | What happened                                     | Fix                                          |
| -------------------------------------------------------------- | ------------------------------------------------- | -------------------------------------------- |
| "the token-encryption key … is missing"                        | the key file was deleted or not restored with the database | `lmm auth login <source>` for each source     |
| "the token-encryption key … is readable by other users"        | the key file's mode was widened                   | `chmod 600 ~/.local/share/lmm/key`           |
| "the token-encryption key … is not a 32-byte key"              | the file was truncated or replaced                | move it aside, then log in again              |
| "the stored credential for … could not be decrypted"           | that one row is damaged, or predates a replaced key | `lmm auth login <that source>`                |

Every one of these is reported per source: a single damaged credential does
not stop the other sources from working, and does not stop lmm from doing
anything that needs no credential at all.

## File permissions

- The data directory (`$XDG_DATA_HOME/lmm`) is `0700`.
- `lmm.db` and its `-wal`/`-shm` sidecars are `0600`, re-applied on every
  open so databases predating the rule are tightened too.
- The token-encryption key is `0600`, and lmm refuses to use one that is
  not.

## `lmm serve`

The local web UI's own posture — no authentication, loopback bind, the
`Host`/`Origin`/CSRF guards and the CSP — is documented in the README under
[Security posture](../README.md#security-posture).

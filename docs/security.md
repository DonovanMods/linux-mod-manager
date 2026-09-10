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

That promise needs the obligation to outlive the attempt, so it is written
down rather than inferred. `VACUUM` cannot run inside a transaction, so the
rows are already encrypted by the time the file cleanup starts; the
transaction that encrypts them therefore also records
`token_scrub_pending` in the database's own `db_meta` table, and that
marker is deleted only once the rebuild has returned **and** the
checkpoint's own result columns have proved the log is empty. Every open
asks the marker — not the rows — whether there is cleanup owed, and honours
it before anything reads a credential. So:

- an open that cannot finish the cleanup fails, however many times it has
  already failed. It never reports success on a second try just because the
  rows now look encrypted.
- the next open with the database to itself finishes the job, and only then
  is the marker cleared.
- nothing is ever reported as encrypted at rest while the pre-encryption
  bytes can still be in `lmm.db` or `lmm.db-wal`.

The wait is bounded and it tells you it is happening: lmm prints one line
when it starts waiting for the other process (up to 45 seconds) and one
when that ends, on stderr, whatever your `--log-level`.

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
*show* a stored credential. They report presence, when it was stored and
last replaced, and a **fingerprint** — the first 8 hex digits of the key's
SHA-256 — which is enough to answer "is this still the key I pasted?" and
tells an attacker nothing. A key supplied through an environment variable
is a different case: lmm holds it in the clear regardless, so that row
still shows the familiar masked `abc...xyz` form alongside its fingerprint.

Being exact about that, because it is a security claim: computing the
fingerprint means decrypting the row, so building one of these documents
does put the key in the process's own memory for as long as it takes to
hash it. It has to — the fingerprint is over the key itself, which is the
only reason a stored row and an environment row can be compared at all.
What the key never does is leave the process: it is not returned by the
listing API (`db.TokenInfo` has no field to put it in), not rendered, not
logged, and not written anywhere.

Only the sources themselves get the key in the clear *outbound*, at the
moment they put it on a request.

### When something goes wrong

| Symptom                                                        | What happened                                     | Fix                                          |
| -------------------------------------------------------------- | ------------------------------------------------- | -------------------------------------------- |
| "the token-encryption key … is missing"                        | the key file was deleted or not restored with the database | `lmm auth login <source>` for each source     |
| "the token-encryption key … is readable by other users"        | the key file's mode was widened                   | `chmod 600 ~/.local/share/lmm/key`           |
| "the token-encryption key … is not a 32-byte key"              | the file was truncated or replaced                | move it aside, then log in again              |
| "the stored credential for … could not be decrypted"           | that one row is damaged, or predates a replaced key | `lmm auth login <that source>`                |

The last row is the per-source case: a single damaged credential is named,
the other sources are still listed and still work, and nothing that needs
no credential is affected at all.

The first three are **not** per-source, and it would be misleading to say
they were. They are about the key file, so they are about every stored
credential at once: `lmm auth status` reports the one problem and the one
remedy instead of listing sources, and `GET /api/v1/auth` answers 500 with
the same message (the web UI shows it in place of the Auth section). That
is deliberate — when the key file is gone, "your key file is gone, here is
the fix" is the whole answer, and repeating "unreadable" once per source
would bury it. Anything that needs no credential keeps working throughout.

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

## External tools

lmm shells out to two programs it does not bundle and never installs for
you: `7z` (to extract `.7z`/`.rar` archives) and `steamcmd` (to download a
Steam Workshop item — #269 Tier 3). Both are probed at the moment they are
needed, and a missing one is an answer with an install hint, not a crash.

`steamcmd` is the one that gets a threat model of its own, because it is a
program that goes looking for a Steam installation:

- **It is always pinned to lmm's own staging directory.** Every invocation
  passes `+force_install_dir <staging>` before `+login`. Without the pin,
  steamcmd resolves your real Steam library and writes into it — this is
  observed behaviour, not a theoretical risk, and the test suite's fake
  steamcmd fails the build if the pin is ever missing or ordered after the
  login.
- **Its environment is built, not filtered.** lmm hands it an `HOME` of
  `$XDG_DATA_HOME/lmm/cache/_steamworkshop/steamcmd-home` with the `XDG_*`
  variables pointed inside it, plus `PATH` and (for display only) `TERM`
  and `LANG`. Nothing else is passed through, so no variable naming your
  real Steam install — `STEAM_ROOT`, `STEAM_COMPAT_*`, your actual `HOME` —
  can reach it by being forgotten. That directory is lmm's to create and
  yours to delete at any time; it is persistent only so steamcmd's ~200 MB
  self-bootstrap is paid once rather than per download.
- **It only ever logs in anonymously.** lmm has no Steam account
  credential of any kind: no password, no cached session, no fallback that
  signs in as you. When a publisher has not opted its app into anonymous
  Workshop downloads, lmm reports the refusal and points at subscribing in
  the Steam client instead.
- **A run is bounded.** 30 minutes, cancellable, with only the tail of its
  output retained for the error message.

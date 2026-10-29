# Release signing

Maintainer notes for the key that signs lmm releases (#569). `lmm self-update`
installs a release only when its `checksums.txt` carries a valid signature by
a key compiled into lmm, so a release published without one, or with one by
a key lmm does not trust, is a release no self-managed lmm can install.

## What is signed, and how it is checked

- The release workflow (`.github/workflows/release.yml`) runs GoReleaser,
  whose `signs:` entry in `.goreleaser.yaml` runs
  `go run ./tools/releasekey sign -in checksums.txt -out checksums.txt.sig`.
  The release publishes `checksums.txt.sig` beside `checksums.txt`.
- The signature is ed25519 over a fixed context string followed by the file
  (`internal/releasesig`), standard base64. Standard library only: cosign
  keyless was rejected because verifying it in Go needs `sigstore-go`, a large
  dependency tree.
- `lmm self-update` verifies the signature against every key in
  `trustedReleaseKeys` (`internal/core/self_update.go`) and accepts it when
  any one verifies. Only then does it check the tarball's sha256 against its
  line in `checksums.txt`. The tarball's name carries the version, so a
  genuinely signed `checksums.txt` from an older release has no line for a
  newer tarball and is refused.

## The key

The private key is the 32-byte ed25519 seed, base64-encoded. It was made once
with:

```bash
go run ./tools/releasekey generate -out release.key
```

which writes the key with mode 0600, never over an existing file, and prints
the public key. The private key then went to two places, and the working copy
was deleted:

- the **`LMM_RELEASE_SIGNING_KEY` secret of the `release` environment** on
  GitHub (`gh secret set LMM_RELEASE_SIGNING_KEY --env release < release.key`),
  which the release workflow passes to GoReleaser. It is an environment
  secret, not a repository one, so only a job that names
  `environment: release` - and passes that environment's rules, below - can
  read it. The workflow fails before building anything when the secret is
  empty;
- an **offline backup** (a password manager is fine). Losing it means a
  rotation; there is no way to recover it from the public key.

The public key is in `trustedReleaseKeys`. The current one is
`NFjV1hUuRPm3P/3iQ+Ohyfqmzty2G27mLpaCdsdJGkg=`. Print the public key of a
private key file with `go run ./tools/releasekey public -key release.key`, and
the keys a build of lmm trusts with `go run ./tools/releasekey trusted`.

## Where the key can be used

Anything that can run a workflow with the key can sign a release every
self-managed lmm will install, so four things stand between the key and a
commit nobody reviewed. Each covers a gap the others leave: a workflow runs
the file at the commit it was started from, so a check written in the
workflow binds only together with the repository settings.

- **The `release` environment** (Settings → Environments → `release`) holds
  the secret. Its deployment policy admits only tags matching `v*` and the
  `main` branch (main for the key check below), so a run from any other
  branch or tag is never given the key, whatever its workflow file says.
  It has a **required reviewer**, a maintainer, so every run that would
  use the key waits for a person to approve it.
- **A tag ruleset** (Settings → Rules → Rulesets) covers `refs/tags/v*`:
  only repository admins may create, update or delete a release tag, and
  a tag cannot be moved once pushed. Without it, anyone who could push a
  tag could start a release from any commit.
- **The main check.** `release.yml`'s first job, `on-main`, holds no
  secret and needs no approval; it fetches `main` and fails the run unless
  `git merge-base --is-ancestor "$GITHUB_SHA" origin/main` - the tagged
  commit is on main. The `release` job, which signs, `needs:` it, so a
  reviewer is only ever asked about a tag on main. GoReleaser's signing
  step runs `tools/releasekey` from that same tagged commit, which is code
  that reached main through a reviewed PR.
- **Least privilege.** Both workflows grant nothing at the top
  (`permissions: {}`); the release job has `contents: write`, which
  publishing the release needs, and every other job `contents: read` or
  nothing.

### Approving a release

Pushing the `vX.Y.Z` tag starts the release workflow. `on-main` runs first;
when it passes, the `release` job waits for approval (Actions → the run →
**Review deployments** → `release` → **Approve and deploy**). Before
approving, check that the run is for the tag you just pushed and that the
tag is on `main`'s release merge commit. Reject anything you did not start.

## Checking the secret

The **Check release signing key** workflow
(`.github/workflows/check-release-key.yml`) runs only when started by hand,
from `main` (Actions → Check release signing key → Run workflow, or
`gh workflow run check-release-key.yml --ref main`); started from any other
ref it fails without touching the key. Its job runs in the `release`
environment, so it waits for the same approval as a release. It signs a throwaway file with the
secret, verifies the signature against each key `releasekey trusted` prints,
and prints `OK` when one verifies and `FAIL` otherwise. It never prints the
key. Run it after setting or changing the secret, and before a release that
follows a rotation step.

## Rotating the key

The list of trusted keys exists so a rotation can overlap: lmm already in
people's hands must trust the new key before any release is signed with it.

1. Generate the new key: `go run ./tools/releasekey generate -out new.key`.
   Back it up offline.
2. Add its public key to `trustedReleaseKeys`, beside the old one, and ship
   a release. That release is still signed with the **old** key, which
   every installed lmm trusts, and it carries the new key to them.
3. Switch the secret:
   `gh secret set LMM_RELEASE_SIGNING_KEY --env release < new.key`, then
   run the check workflow and confirm `OK`. Delete the working copy of
   `new.key`.
4. Ship the next release, signed with the new key. An lmm from step 2 or
   later verifies it; one older than step 2 cannot, and its user updates by
   hand once.
5. One release later, remove the old public key from `trustedReleaseKeys`,
   and destroy the old key's backup.

If the private key leaks, skip the overlap: remove the leaked key from the
list in the same release that adds the new one, sign that release with the
new key, and say in its notes that self-managed installs must update by hand
once.

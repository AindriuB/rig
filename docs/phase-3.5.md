# Phase 3.5: self-update and hardening

Status: **0.5.2 is built** (this document's first part). The 0.6.0 part is
added by a later task.

## 0.5.2: self-update

### Goals

- `rig update` replaces the running rig with the latest signed release, or one
  named with `--to`, and `--check` only reports.
- Release binaries are signed from 0.5.2, so rig can prove what it installs.
- `update.auto` lets a user choose to be told about, or to receive, new
  releases without running a command.

### Decisions

- **Ed25519 signatures over `SHA256SUMS`.** The release signs the checksum
  file once; the checksum of each binary ties it to that signature.
- **A raw 64-byte `.sig`.** `SHA256SUMS.sig` holds the signature bytes, with no
  encoding or armour.
- **Key rotation by list.** rig embeds a list of public keys and accepts a
  signature from any one of them, so a new key can ship before the old one is
  retired.
- **Verification order:** signature, then checksum, then a run of the staged
  binary's `version`, which must print the target version. Nothing replaces the
  installed file until all three pass.
- **Replacement.** The new file is staged beside the old one and renamed over
  it. On Unix a running process keeps the old inode; on Windows the old file
  moves to `<rig>.old` and is removed on a later run. A `go install`ed copy is
  replaced by the release binary built from the same tag.
- **Releases before 0.5.2 are refused**: they are unsigned. A latest release
  older than the running rig is refused unless `--to` names it.
- **`update.auto`:** `off` (default), `notify` or `install`. Global file only,
  so a repository cannot turn it on. At most one check per 24 h, run after the
  command, with a 2 s timeout, one stderr line, and never a change of exit code.
  Silent in CI, without a terminal, with `--json`, and offline. A lock file
  stops two processes installing at once.

### Security and trust

- One fixed release source (the `AindriuB/rig` GitHub releases). No flag,
  environment variable or config key changes it.
- https only, to a short allow-list of GitHub hosts, checked again on every
  redirect. Downloads have size limits. Drafts and prereleases are ignored.
- The trust root is the public key list compiled into rig. A compromised
  release page cannot make rig install an unsigned or mismatched binary.
- A failed check leaves the installed file untouched.

### Out of scope

- Updating from any other source, a mirror or a private build.
- Downgrading below 0.5.2, and rolling back after an install.
- Signing anything other than release checksums.

### Tasks

1. 01 release-signing: sign `SHA256SUMS` in the release workflow.
2. 02 selfupdate-fetch-verify: find, download and verify a signed release.
3. 03 selfupdate-replace: stage and atomically replace the binary.
4. 04 rig-update-command: `rig update`.
5. 05 update-auto-mode: `update.auto` and the post-command pass.
6. 06 docs-052: these docs.

## 0.6.0

To be written by task 14.

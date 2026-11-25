# Release runbook

<!-- Read by /ship. Keep it under 30 lines: commands and checks, no prose. -->

## Version bump
- File: `cmd/rig/version.go` — set `version` to `<version>`.
- Changelog: in the release commit, move `## [Unreleased]` entries of `CHANGELOG.md` under `## [<version>] - <date>` and add its compare link, before the tag.
- Commit: `release: <version>` on a `task/release-<version>` branch; PR to `main`.
- Wait: CI `ci.yml` green on the PR, then merge.
- Skip when `go run ./cmd/rig version` on `main` already prints `<version>`.

## Tag
- `git tag -a v<version> -m "<version>"` on `main`'s head, then `git push origin v<version>`.
- `release.yml` refuses a tag that does not match `rig version`.

## Publish targets
| Target | Triggered by | Workflow | Secret it needs |
|---|---|---|---|
| GitHub release (draft, 5 binaries + SHA256SUMS + SHA256SUMS.sig) | tag push | `release.yml` | `RIG_SIGNING_KEY` (Ed25519 seed, base64; make with `go run ./tools/rigsign keygen <dir>`) |
| GitHub release published | `gh release edit v<version> --draft=false --latest` | none (manual) | none |
| rig-site rebuild | GitHub release published | `site-dispatch.yml` | `RIG_SITE_DISPATCH_TOKEN` |

## Verify the published state
- `gh release view v<version> --json isDraft,assets --jq '.isDraft, [.assets[].name]'`
  prints `false` and `rig-{linux-amd64,linux-arm64,darwin-amd64,darwin-arm64,windows-amd64.exe}`, `SHA256SUMS`, `SHA256SUMS.sig`.
- `go install github.com/AindriuB/rig/cmd/rig@v<version>` in a temp `GOBIN`, then `rig version` prints `<version>`.

## If a step fails
- Do not delete or move a pushed tag. Fix forward with `<version+patch>`.

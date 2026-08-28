# Homebrew publishing

Výbava publishes precompiled archives with GoReleaser and installs them as a
Homebrew cask. Repo, release assets, and tap are all public — installs need no
authentication.

## Cutting a release

```sh
scripts/release.sh          # minor bump (default)
scripts/release.sh patch    # or major
```

The script releases from a clean, synced `main`: it computes the next tag,
runs `goreleaser check` when available, tags, pushes, and watches the Release
workflow to green.

## What the workflow does

1. A `v*` tag starts `.github/workflows/release.yml`.
2. GoReleaser builds archives + checksums and creates the GitHub release.
3. GoReleaser generates `Casks/vybava.rb` (plain public download URL, verified
   against this repo) and pushes it to `FixIt-Technologies/homebrew-tap` using
   the repository-scoped SSH deploy key in the `TAP_DEPLOY_KEY` Actions secret.
4. The tap's ruleset lets only deploy keys bypass its PR requirement; human
   changes to the tap still go through PRs.

The deploy key has write access only to `homebrew-tap`. Do not replace it with
a personal access token. Rotate it by adding a new write deploy key to the tap,
replacing the `TAP_DEPLOY_KEY` secret in `vybava`, running a release, then
deleting the old key.

## Install and verify

```sh
brew install --cask FixIt-Technologies/tap/vybava
vybava --version
vybava doctor
```

Upgrades: `brew upgrade --cask vybava`.

History: until 2026-08-28 the tap was private and the cask used a token-gated
custom download strategy (`HOMEBREW_GITHUB_API_TOKEN`). Casks generated since
use a plain URL; if an old install complains about a missing token, upgrade.

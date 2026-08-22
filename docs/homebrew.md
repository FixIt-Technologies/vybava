# Homebrew publishing

Výbava publishes precompiled archives with GoReleaser and installs them as a
Homebrew cask. GoReleaser's legacy binary-formula publisher is intentionally
not used.

## Release flow

1. A `v*` tag starts `.github/workflows/release.yml`.
2. GoReleaser builds archives and checksums and creates the private GitHub
   release.
3. GoReleaser generates `Casks/vybava.rb` and pushes it directly to
   `FixIt-Technologies/homebrew-tap` using the repository-scoped SSH deploy key
   held in the `TAP_DEPLOY_KEY` Actions secret.
4. The tap's repository ruleset allows only deploy keys to bypass its normal
   pull-request requirement. Human changes still use PRs.

The deploy key has write access only to `homebrew-tap`. Do not replace it with
a personal access token. Rotate it by adding a new write deploy key to the tap,
replacing the `TAP_DEPLOY_KEY` secret in `vybava`, running a release, and then
deleting the old deploy key.

## Private download contract

Homebrew must authenticate twice:

- Git credentials clone the private tap. `gh auth setup-git` configures this.
- `HOMEBREW_GITHUB_API_TOKEN` lets the generated cask download assets from the
  private `vybava` release.

Homebrew scrubs sensitive environment variables while loading a cask. The
generated cask therefore uses a custom download strategy that reads the token
only when the download begins. Do not simplify it to a static `url.headers`
entry; that breaks on current Homebrew.

## Install and verify

```sh
gh auth setup-git
export HOMEBREW_GITHUB_API_TOKEN="$(gh auth token)"
brew install --cask FixIt-Technologies/tap/vybava
vybava --version
vybava doctor
```

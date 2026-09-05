# ci/ — Výbava for pipelines and images

Everything a CI runner image, a GitHub workflow or a provisioning script needs
from this repository lives in this directory. **Nothing outside `ci/` is a
pipeline contract**: `scripts/`, `skills/`, `docs/` and the Go sources are
internal, and a pipeline must never `actions/checkout` this repository or
`go build` it to obtain an applet.

## `install.sh`

Installs a tagged release — the same archive the Homebrew cask ships — after
verifying it against the release's `checksums.txt`, then links the applets
you name. Pin the script ref and `--version` to the same tag:

```sh
curl -fsSL https://raw.githubusercontent.com/FixIt-Technologies/vybava/v0.3.3/ci/install.sh \
  | bash -s -- --version 0.3.3 --bin-dir /usr/local/bin --install memorylint,hotfix
```

| Flag | Meaning |
|---|---|
| `--version x.y.z` | release to install (required unless `--from-dir`) |
| `--bin-dir <dir>` | destination for `vybava` and applet links (default `/usr/local/bin`) |
| `--install a,b` | catalog items to install afterwards; applets link into `--bin-dir`, skills go to the agent home |
| `--agent claude\|codex\|all` | skill target (default `all`); irrelevant when only applets are named |
| `--from-dir <dir>` | local archive + `checksums.txt` instead of GitHub (offline, tests) |

Consumers today: `devulinka-infra` `apps/gh-runner/Dockerfile` (every Docker
JIT CI lane), `apps/devbox-vm/scripts/deploy-guest.sh` (the Devbox guest),
and FixIt's `memory-hygiene.yml` as its fallback when the runner image predates
the version it wants.

Bumping: cut the release (`scripts/release.sh`), then move the pins in those
consumers. A consumer never floats on `main`.

## Adding to this directory

Only pipeline-facing entry points belong here, each with the same contract as
`install.sh`: non-interactive, idempotent, exits non-zero with one line on
failure, tested from `internal/ciinstall`.

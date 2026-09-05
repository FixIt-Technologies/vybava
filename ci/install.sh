#!/usr/bin/env bash
# ci/install.sh — the ONE way CI images, workflows and provisioning scripts get
# Výbava. Downloads a tagged release archive (never a repo checkout — the repo
# carries internal scripts, skills and docs that no pipeline needs), verifies it
# against the release's checksums.txt, installs the multicall `vybava` binary,
# and optionally links the requested applets / installs the requested skills.
#
#   curl -fsSL -o /tmp/vybava-install.sh \
#     https://raw.githubusercontent.com/FixIt-Technologies/vybava/v0.3.3/ci/install.sh \
#   && bash /tmp/vybava-install.sh --version 0.3.3 --bin-dir /usr/local/bin --install memorylint,hotfix
#
# Download, THEN run — never `curl … | bash`: without pipefail a 404 hands bash
# an empty script and the step exits 0 having installed nothing. Pin BOTH the
# script ref and --version to the same tag. Idempotent: re-running with the
# same version replaces the binary and re-links the applets.
#
#   --version <x.y.z>     release to install (required unless --from-dir)
#   --bin-dir <dir>       where `vybava` and applet links go (default /usr/local/bin)
#   --install <a,b,...>   catalog items to install after the binary (default: none)
#   --agent <claude|codex|all>
#                         skill target for items that are skills (default all)
#   --from-dir <dir>      take <archive> + checksums.txt from a local directory
#                         instead of GitHub (offline installs, the test suite)
#   --repo <owner/name>   release source (default FixIt-Technologies/vybava)
set -euo pipefail

version=""
bin_dir="/usr/local/bin"
items=""
agent="all"
from_dir=""
repo="FixIt-Technologies/vybava"

die() { printf 'vybava ci/install.sh: %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="${2:?--version needs a value}"; shift 2 ;;
    --bin-dir) bin_dir="${2:?--bin-dir needs a value}"; shift 2 ;;
    --install) items="${2:?--install needs a value}"; shift 2 ;;
    --agent) agent="${2:?--agent needs a value}"; shift 2 ;;
    --from-dir) from_dir="${2:?--from-dir needs a value}"; shift 2 ;;
    --repo) repo="${2:?--repo needs a value}"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo pipefail/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "$agent" in claude|codex|all) ;; *) die "--agent must be claude, codex or all" ;; esac
[[ -n "$version" || -n "$from_dir" ]] || die "--version is required (or --from-dir for a local archive)"
version="${version#v}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) die "unsupported OS: $os" ;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

if [[ -n "$from_dir" ]]; then
  archive=$(find "$from_dir" -maxdepth 1 -name "vybava_*_${os}_${arch}.tar.gz" 2>/dev/null | head -1)
  [[ -n "$archive" ]] || die "no vybava_*_${os}_${arch}.tar.gz in $from_dir"
  cp -- "$archive" "$from_dir/checksums.txt" "$work/"
  archive_name=$(basename "$archive")
else
  archive_name="vybava_${version}_${os}_${arch}.tar.gz"
  base="https://github.com/${repo}/releases/download/v${version}"
  curl -fsSL --retry 3 -o "$work/$archive_name" "$base/$archive_name" \
    || die "download failed: $base/$archive_name"
  curl -fsSL --retry 3 -o "$work/checksums.txt" "$base/checksums.txt" \
    || die "download failed: $base/checksums.txt"
fi

# checksums.txt lists every asset; verify only ours, with whichever sha256 tool
# the image has (coreutils on Linux, perl shasum on macOS).
expected=$(awk -v name="$archive_name" '$2 == name { print $1 }' "$work/checksums.txt")
[[ -n "$expected" ]] || die "$archive_name is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/$archive_name" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$work/$archive_name" | awk '{ print $1 }')
fi
[[ "$actual" == "$expected" ]] || die "checksum mismatch for $archive_name (expected $expected, got $actual)"

tar -xzf "$work/$archive_name" -C "$work" vybava
install -d -m 0755 -- "$bin_dir"
install -m 0755 -- "$work/vybava" "$bin_dir/vybava"

if [[ -n "$items" ]]; then
  # shellcheck disable=SC2086  # comma list → words on purpose
  "$bin_dir/vybava" install ${items//,/ } --bin-dir "$bin_dir" --agent "$agent"
fi

# The installed binary must actually run on this host — an archive for the
# wrong libc or a truncated extract must fail the install, not print success.
version_line=$("$bin_dir/vybava" --version) || die "$bin_dir/vybava does not run on this host"
printf 'installed %s → %s/vybava' "$version_line" "$bin_dir"
[[ -n "$items" ]] && printf ' (+ %s)' "$items"
printf '\n'

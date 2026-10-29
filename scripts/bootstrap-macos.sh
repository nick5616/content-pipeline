#!/usr/bin/env bash
# Installs Canon's dev toolchain on macOS with Homebrew. Safe to re-run: it
# lists what's missing, then installs only that.
#
#   bash scripts/bootstrap-macos.sh
#
# WSL is the canonical job runner (the blob cache lives there). Brew's ffmpeg
# and exiftool versions differ from Ubuntu's; `canon doctor` records both, and
# sanitized bytes can differ between the two machines. Ingest dedupes by the
# original's hash, so the same photo ingested on both is still one item.
set -euo pipefail

[[ "$(uname -s)" == Darwin ]] || { echo "this script is for macOS"; exit 1; }
command -v brew >/dev/null || { echo "install Homebrew first: https://brew.sh"; exit 1; }

FORMULAE=(go go-task fnm ffmpeg exiftool qpdf libheif vips jq)
CASKS=(google-cloud-sdk)
NODE_VERSION=24.22.1

missing=()
for f in "${FORMULAE[@]}"; do brew list --formula "$f" >/dev/null 2>&1 || missing+=("$f"); done
missing_casks=()
for c in "${CASKS[@]}"; do brew list --cask "$c" >/dev/null 2>&1 || missing_casks+=("$c"); done

echo "==> Homebrew"
if ((${#missing[@]} + ${#missing_casks[@]})); then
  echo "will install: ${missing[*]} ${missing_casks[*]}"
  ((${#missing[@]})) && brew install "${missing[@]}"
  ((${#missing_casks[@]})) && brew install --cask "${missing_casks[@]}"
else
  echo "all present"
fi

echo "==> Node $NODE_VERSION (fnm)"
eval "$(fnm env --shell bash)"
fnm install "$NODE_VERSION"
fnm default "$NODE_VERSION"

echo "==> summary"
for t in go node npm task ffmpeg ffprobe exiftool qpdf heif-dec heif-enc vips jq gcloud docker; do
  printf '  %-9s %s\n' "$t" "$(command -v "$t" || echo MISSING)"
done
cat <<'MSG'

If fnm isn't on your PATH in new shells, add this to ~/.zshrc:
  eval "$(fnm env --use-on-cd --shell zsh)"
MSG

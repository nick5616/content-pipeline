#!/usr/bin/env bash
# Installs Canon's dev toolchain on WSL Ubuntu 24.04. Safe to re-run: every step
# checks what is already there and only installs what is missing or outdated.
#
#   bash scripts/bootstrap-wsl.sh
#
# Uses sudo only for apt (ffmpeg, exiftool, qpdf, libheif, jq, gcloud). Everything else goes
# under ~/.local, and the one line it adds to ~/.bashrc sources ~/.config/canon/env.sh.
set -euo pipefail

GO_VERSION=1.27.3
GO_SHA256=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 # linux-amd64, from go.dev/dl
NODE_VERSION=24.22.1
FNM_VERSION=1.39.0
TASK_VERSION=3.54.0

LOCAL="$HOME/.local"
ENV_FILE="$HOME/.config/canon/env.sh"
step() { printf '\n==> %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1 && [[ "$(command -v "$1")" != /mnt/* ]]; }

[[ "$(uname -m)" == x86_64 ]] || { echo "expected x86_64, got $(uname -m)"; exit 1; }
[[ "$(id -u)" != 0 ]] || { echo "run as your normal user, not root (sudo is used where needed)"; exit 1; }
mkdir -p "$LOCAL/bin" "$(dirname "$ENV_FILE")"

step "apt packages"
APT_PKGS=(ffmpeg libimage-exiftool-perl qpdf libheif-examples jq unzip curl ca-certificates gnupg)
missing=()
for p in "${APT_PKGS[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if ((${#missing[@]})); then
  sudo apt-get update -q
  sudo apt-get install -y -q "${missing[@]}"
else
  echo "all present"
fi

step "gcloud CLI (Linux-native, so ADC lands in ~/.config/gcloud where Go looks)"
if dpkg -s google-cloud-cli >/dev/null 2>&1; then
  echo "present"
else
  sudo install -d -m 0755 /usr/share/keyrings
  curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
    | sudo gpg --dearmor --yes -o /usr/share/keyrings/cloud.google.gpg
  echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
    | sudo tee /etc/apt/sources.list.d/google-cloud-sdk.list >/dev/null
  sudo apt-get update -q
  sudo apt-get install -y -q google-cloud-cli
fi

step "Go $GO_VERSION"
if [[ -x "$LOCAL/go/bin/go" ]] && "$LOCAL/go/bin/go" version | grep -q "go$GO_VERSION "; then
  echo "present"
else
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/go.tgz" "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz"
  echo "$GO_SHA256  $tmp/go.tgz" | sha256sum -c -
  rm -rf "$LOCAL/go"
  tar -C "$LOCAL" -xzf "$tmp/go.tgz"
  rm -rf "$tmp"
fi
export PATH="$LOCAL/go/bin:$LOCAL/bin:$HOME/go/bin:$PATH"

step "Task $TASK_VERSION"
if have task && task --version 2>/dev/null | grep -q "$TASK_VERSION"; then
  echo "present"
else
  GOBIN="$LOCAL/bin" go install "github.com/go-task/task/v3/cmd/task@v$TASK_VERSION"
fi

step "fnm $FNM_VERSION and Node $NODE_VERSION"
if [[ -x "$LOCAL/bin/fnm" ]] && "$LOCAL/bin/fnm" --version | grep -q "$FNM_VERSION"; then
  echo "fnm present"
else
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/fnm.zip" "https://github.com/Schniz/fnm/releases/download/v$FNM_VERSION/fnm-linux.zip"
  unzip -q -o "$tmp/fnm.zip" -d "$LOCAL/bin"
  chmod +x "$LOCAL/bin/fnm"
  rm -rf "$tmp"
fi
eval "$("$LOCAL/bin/fnm" env --shell bash)"
fnm install "$NODE_VERSION"
fnm default "$NODE_VERSION"

step "shell environment"
cat >"$ENV_FILE" <<EOF
# Written by content-pipeline/scripts/bootstrap-wsl.sh. Linux tools first, so the
# Windows npm/gcloud that WSL appends to PATH never win.
export PATH="$LOCAL/go/bin:$LOCAL/bin:\$HOME/go/bin:\$PATH"
eval "\$("$LOCAL/bin/fnm" env --use-on-cd --shell bash)"
EOF
line="[ -f \"$ENV_FILE\" ] && . \"$ENV_FILE\""
grep -qxF "$line" "$HOME/.bashrc" || echo "$line" >>"$HOME/.bashrc"
echo "wrote $ENV_FILE (sourced from ~/.bashrc)"

step "summary"
# shellcheck disable=SC1090
. "$ENV_FILE"
for t in go node npm task ffmpeg ffprobe exiftool qpdf heif-dec heif-enc jq gcloud docker; do
  if p=$(command -v "$t" 2>/dev/null); then
    [[ "$p" == /mnt/* ]] && flag="  <-- Windows binary, not Linux" || flag=""
    printf '  %-9s %s%s\n' "$t" "$p" "$flag"
  else
    printf '  %-9s MISSING\n' "$t"
  fi
done
echo "  versions: $(go version | cut -d' ' -f3), node $(node -v), task $(task --version | grep -o 'v\?[0-9.]*' | head -1), ffmpeg $(ffmpeg -version | head -1 | cut -d' ' -f3), exiftool $(exiftool -ver), qpdf $(qpdf --version | head -1 | cut -d' ' -f3)"

if docker info >/dev/null 2>&1; then
  echo "  docker daemon reachable"
else
  echo "  docker daemon NOT reachable: in Docker Desktop, Settings > Resources > WSL integration, turn on Ubuntu (needed for integration tests)"
fi

cat <<'EOF'

Optional: to stop Windows binaries leaking into WSL entirely, add this to /etc/wsl.conf
and run `wsl --terminate Ubuntu` from PowerShell:
  [interop]
  appendWindowsPath=false

Done. Open a new shell (or `source ~/.bashrc`) to pick up PATH.
EOF

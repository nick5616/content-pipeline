#!/usr/bin/env bash
# Creates or updates Canon's three buckets and its service account so they
# match the plan (docs/plans/M0-M1.md, section 1.4). Safe to re-run: every
# step reads current state first, and a second run reports "no changes".
#
#   scripts/provision-buckets.sh                 # dry run: print what would change
#   scripts/provision-buckets.sh --apply         # make the changes
#
# Values come from the Canon config (canon config env), then flags override:
#   --config PATH  --project ID  --region LOC  --private-bucket NAME
#   --public-bucket NAME  --archive-bucket NAME  --service-account EMAIL
#   --operator user:EMAIL  --cors-origin URL (repeatable; replaces config list)
#
# There are no defaults. If anything is unset it exits 2 before calling gcloud.
# It never touches gs://artandmedia, gs://artandmedia-originals or their
# private copies (D42).
set -euo pipefail

MODE=dry-run
CONFIG=""
declare -A FLAG=()
FLAG_CORS=()

usage() { sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; }
need_value() { [[ $# -ge 2 && -n "$2" ]] || { echo "error: $1 needs a value" >&2; exit 2; }; }

while (($#)); do
  case "$1" in
    --dry-run) MODE=dry-run ;;
    --apply) MODE=apply ;;
    --config) need_value "$@"; CONFIG=$2; shift ;;
    --project | --region | --private-bucket | --public-bucket | --archive-bucket | --service-account | --operator)
      need_value "$@"; FLAG[${1#--}]=$2; shift ;;
    --cors-origin) need_value "$@"; FLAG_CORS+=("$2"); shift ;;
    -h | --help) usage; exit 0 ;;
    *) echo "error: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

# --- Resolve settings -------------------------------------------------------

CANON_PROJECT="" CANON_REGION="" CANON_PRIVATE_BUCKET="" CANON_PUBLIC_BUCKET=""
CANON_ARCHIVE_BUCKET="" CANON_SERVICE_ACCOUNT="" CANON_OPERATOR="" CANON_CORS_ORIGINS=""

canon_cmd() {
  if [[ -n "${CANON_BIN:-}" ]]; then
    "$CANON_BIN" "$@"
  elif command -v canon >/dev/null 2>&1; then
    canon "$@"
  else
    (cd "$(dirname "$0")/.." && go run ./cmd/canon "$@")
  fi
}

default_config="${XDG_CONFIG_HOME:-$HOME/.config}/canon/config.yaml"
if [[ -n "$CONFIG" || -f "$default_config" ]]; then
  env_out=$(canon_cmd config env ${CONFIG:+--config "$CONFIG"}) || { echo "error: could not read config" >&2; exit 2; }
  eval "$env_out"
fi

PROJECT=${FLAG[project]:-$CANON_PROJECT}
REGION=${FLAG[region]:-$CANON_REGION}
PRIVATE=${FLAG[private-bucket]:-$CANON_PRIVATE_BUCKET}
PUBLIC=${FLAG[public-bucket]:-$CANON_PUBLIC_BUCKET}
ARCHIVE=${FLAG[archive-bucket]:-$CANON_ARCHIVE_BUCKET}
SA=${FLAG[service-account]:-$CANON_SERVICE_ACCOUNT}
OPERATOR=${FLAG[operator]:-$CANON_OPERATOR}
if ((${#FLAG_CORS[@]})); then
  CORS_ORIGINS=("${FLAG_CORS[@]}")
else
  read -r -a CORS_ORIGINS <<<"$CANON_CORS_ORIGINS"
fi

missing=()
for v in PROJECT REGION PRIVATE PUBLIC ARCHIVE SA OPERATOR; do [[ -n "${!v}" ]] || missing+=("$v"); done
((${#CORS_ORIGINS[@]})) || missing+=(CORS_ORIGINS)
if ((${#missing[@]})); then
  echo "error: unset: ${missing[*]} (set them in config or pass flags; see --help)" >&2
  exit 2
fi
for b in "$PRIVATE" "$PUBLIC" "$ARCHIVE"; do
  case "$b" in artandmedia | artandmedia-originals | artandmedia-copy | artandmedia-originals-copy)
    echo "error: gs://$b is an import source; Canon never writes to it" >&2; exit 2 ;;
  esac
done
if [[ "$PRIVATE" == "$PUBLIC" || "$PRIVATE" == "$ARCHIVE" || "$PUBLIC" == "$ARCHIVE" ]]; then
  echo "error: private, public and archive buckets must all differ" >&2; exit 2
fi
if [[ "$SA" != *"@$PROJECT.iam.gserviceaccount.com" ]]; then
  echo "error: service account $SA is not in project $PROJECT" >&2; exit 2
fi
command -v jq >/dev/null || { echo "error: jq is required" >&2; exit 2; }
command -v gcloud >/dev/null || { echo "error: gcloud is required" >&2; exit 2; }

SOFT_DELETE_SECONDS=2592000 # 30 days
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# --- Helpers ----------------------------------------------------------------

CHANGES=0
mutate() {
  CHANGES=$((CHANGES + 1))
  if [[ $MODE == apply ]]; then
    echo "  + gcloud $*"
    gcloud "$@" >/dev/null
  else
    echo "  would run: gcloud $*"
  fi
}

# describe ARGS... prints JSON, or nothing (exit 0) if the resource is missing.
# Any other failure (no permission, no network) aborts the run.
describe() {
  local out err
  if out=$(gcloud "$@" --format=json 2>"$TMP/err"); then
    printf '%s' "$out"
    return 0
  fi
  err=$(<"$TMP/err")
  if grep -qiE 'not found|404|does not exist|NOT_FOUND' <<<"$err"; then
    return 0
  fi
  echo "error: gcloud $* failed:" >&2
  echo "$err" >&2
  exit 1
}

has_member() { # POLICY_JSON ROLE MEMBER
  jq -e --arg r "$2" --arg m "$3" \
    '[.bindings[]? | select(.role == $r) | .members[]?] | index($m) != null' <<<"$1" >/dev/null
}

cors_json() {
  jq -n --args '[{origin: $ARGS.positional, method: ["GET", "HEAD"], responseHeader: ["Content-Type"], maxAgeSeconds: 3600}]' \
    "${CORS_ORIGINS[@]}"
}
normalize_cors() {
  jq -S '[(. // [])[] | {origin: ((.origin // []) | sort), method: ((.method // []) | sort),
    responseHeader: ((.responseHeader // []) | sort), maxAgeSeconds: (.maxAgeSeconds // 0 | tonumber)}]'
}

# --- Service account --------------------------------------------------------

echo "Mode: $MODE   project: $PROJECT   region: $REGION"
echo
echo "service account $SA"
SA_NAME=${SA%%@*}
sa_exists=1
if [[ -z "$(describe iam service-accounts describe "$SA" --project="$PROJECT")" ]]; then
  sa_exists=0
  mutate iam service-accounts create "$SA_NAME" --project="$PROJECT" --display-name="Canon apply"
fi
sa_policy=""
((sa_exists)) && sa_policy=$(describe iam service-accounts get-iam-policy "$SA" --project="$PROJECT")
if ! has_member "${sa_policy:-"{}"}" roles/iam.serviceAccountTokenCreator "$OPERATOR"; then
  mutate iam service-accounts add-iam-policy-binding "$SA" --project="$PROJECT" \
    --member="$OPERATOR" --role=roles/iam.serviceAccountTokenCreator
fi

# --- Buckets ----------------------------------------------------------------

# bucket NAME ROLE: ROLE is private, public or archive.
bucket() {
  local name=$1 role=$2 url="gs://$1" pap=enforced
  [[ $role == public ]] && pap=inherited
  echo
  echo "$role bucket $url"

  local state fresh=0
  state=$(describe storage buckets describe "$url")
  if [[ -z "$state" ]]; then
    local pap_flag=--public-access-prevention
    [[ $pap == inherited ]] && pap_flag=--no-public-access-prevention
    mutate storage buckets create "$url" --project="$PROJECT" --location="$REGION" \
      --uniform-bucket-level-access "$pap_flag" --soft-delete-duration=30d
    # What a fresh bucket looks like; the update below fills in the rest.
    state=$(jq -n --arg pap "$pap" --arg loc "${REGION^^}" '{location: $loc, uniform_bucket_level_access: true,
      public_access_prevention: $pap, soft_delete_policy: {retentionDurationSeconds: "2592000"}}')
    fresh=1
  else
    local loc
    loc=$(jq -r '.location // ""' <<<"$state")
    if [[ "${loc,,}" != "${REGION,,}" ]]; then
      echo "  warning: location is $loc, config says $REGION (location can't be changed in place)"
    fi
  fi

  local update=()
  [[ $(jq -r '.uniform_bucket_level_access // false' <<<"$state") == true ]] || update+=(--uniform-bucket-level-access)
  [[ $(jq -r '.versioning_enabled // false' <<<"$state") == true ]] || update+=(--versioning)
  [[ $(jq -r '.soft_delete_policy.retentionDurationSeconds // 0 | tonumber' <<<"$state") == "$SOFT_DELETE_SECONDS" ]] ||
    update+=(--soft-delete-duration=30d)
  if [[ $(jq -r '.public_access_prevention // "inherited"' <<<"$state") != "$pap" ]]; then
    [[ $pap == enforced ]] && update+=(--public-access-prevention) || update+=(--no-public-access-prevention)
  fi
  if [[ $role == public ]]; then
    cors_json >"$TMP/cors.json"
    if [[ "$(jq '.cors_config' <<<"$state" | normalize_cors)" != "$(normalize_cors <"$TMP/cors.json")" ]]; then
      update+=(--cors-file="$TMP/cors.json")
    fi
  fi
  if [[ $role == archive ]] && [[ $(jq '.lifecycle_config.rule // [] | length' <<<"$state") != 0 ]]; then
    update+=(--clear-lifecycle) # originals are kept forever (D14)
  fi
  ((${#update[@]})) && mutate storage buckets update "$url" "${update[@]}"

  local policy=""
  ((fresh)) || policy=$(describe storage buckets get-iam-policy "$url")
  policy=${policy:-"{}"}
  local want=()
  case $role in
    private) want=("serviceAccount:$SA roles/storage.objectAdmin") ;;
    public) want=("allUsers roles/storage.objectViewer" "serviceAccount:$SA roles/storage.objectAdmin") ;;
    # Create and read only: the service account can never delete or overwrite an original.
    archive) want=("serviceAccount:$SA roles/storage.objectCreator" "serviceAccount:$SA roles/storage.objectViewer") ;;
  esac
  local w member r
  for w in "${want[@]}"; do
    read -r member r <<<"$w"
    has_member "$policy" "$r" "$member" ||
      mutate storage buckets add-iam-policy-binding "$url" --member="$member" --role="$r"
  done
  if [[ $role == archive ]] && has_member "$policy" roles/storage.objectAdmin "serviceAccount:$SA"; then
    echo "  warning: $SA has objectAdmin on the archive; remove it by hand (this script never removes access)"
  fi
}

bucket "$PRIVATE" private
bucket "$PUBLIC" public
bucket "$ARCHIVE" archive

echo
if ((CHANGES == 0)); then
  echo "No changes: everything matches."
elif [[ $MODE == apply ]]; then
  echo "Applied $CHANGES change(s). Run again to confirm there are none left."
else
  echo "$CHANGES change(s) needed. Re-run with --apply to make them."
fi

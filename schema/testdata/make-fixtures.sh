#!/usr/bin/env bash
# Derives the invalid fixtures from valid/full.json, one rule per file, and
# writes expectations.json. Run by `task gen`; CI fails if the output drifts.
#
# Each case states two verdicts, both checked by Go and the Zod one by Vitest:
#   schemaValid  does the file pass its JSON Schema (Go validator and Zod agree)
#   codes        the sorted problem codes engine/manifest.Load reports
#                ("schema" for schema failures; others are Go-only semantic rules)
set -euo pipefail
cd "$(dirname "$0")"

FULL=valid/full.json
ID() { jq -r ".items[$1].id" "$FULL"; }
EXP=()
rm -f invalid/*.json public/*.json

# case FILE SCHEMA SCHEMA_VALID CODES JQ_FILTER [SOURCE]
case_() {
  local file=$1 schema=$2 valid=$3 codes=$4 filter=$5 src=${6:-$FULL}
  mkdir -p "$(dirname "$file")"
  jq --indent 2 "$filter" "$src" >"$file"
  EXP+=("$(jq -cn --arg f "$file" --arg s "$schema" --argjson v "$valid" --arg c "$codes" \
    '{file: $f, schema: $s, schemaValid: $v, codes: ($c | split(" ") | map(select(. != "")))}')")
}
valid() { EXP+=("$(jq -cn --arg f "$1" --arg s "$2" --arg c "$3" '{file: $f, schema: $s, schemaValid: true, codes: ($c | split(" ") | map(select(. != "")))}')"); }

# The github source link on rpg-quests is a warning in every derived case.
W=link-not-owned

valid valid/full.json manifest "$W"
valid valid/empty.json manifest ""

# Schema rules: caught by the JSON Schema, so Go and Zod must agree.
case_ invalid/bad-blob-ref.json manifest false schema '.items[2].assets[0].blob = "sha256:abc"'
case_ invalid/bad-date-format.json manifest false schema '.items[0].date.value = "June 2025"'
case_ invalid/copy-field-not-allowed.json manifest false schema '.items[0].copy["holodeck-showroom"].tech = ["react"]'
case_ invalid/placement-copy-field-not-allowed.json manifest false schema '.items[1].placements[1].copy.links = []'
case_ invalid/facts-wrong-kind.json manifest false schema '.items[6].facts.embeddable = true'
case_ invalid/facts-bad-camelot.json manifest false schema '.items[6].facts.camelot = "13C"'
case_ invalid/members-on-project.json manifest false schema ".items[0].members = [\"$(ID 2)\"]"
case_ invalid/bad-link-rel.json manifest false schema '.items[0].links[0].rel = "homepage"'
case_ invalid/bad-link-url.json manifest false schema '.items[0].links[0].url = "javascript:alert(1)"'
case_ invalid/unknown-item-field.json manifest false schema '.items[0].hide = true'
case_ invalid/bad-ulid.json manifest false schema '.items[0].id = "not-a-ulid"'
case_ invalid/bad-slug.json manifest false schema '.items[0].slug = "RPG Quests"'
case_ invalid/missing-title.json manifest false schema 'del(.items[0].title)'
case_ invalid/bad-visibility.json manifest false schema '.items[0].visibility = "hidden"'
case_ invalid/owned-domain-with-scheme.json manifest false schema '.ownedDomains += ["https://example.com"]'
case_ invalid/schema-version.json manifest false schema '.schemaVersion = 2'
case_ invalid/private-unknown-field.json manifest false schema '.items[0].private.secret = "x"'

# Semantic rules: the schema passes, engine/manifest.Validate catches them.
case_ invalid/duplicate-id.json manifest true "duplicate-id $W" ".items[1].id = \"$(ID 0)\""
case_ invalid/duplicate-slug.json manifest true "duplicate-slug $W" '.items[1].slug = "rpg-quests"'
case_ invalid/alias-is-other-slug.json manifest true "alias-collision $W" '.items[1].aliases = ["rpg-quests"]'
case_ invalid/alias-is-other-alias.json manifest true "alias-collision $W" '.items[1].aliases = ["videogamequest"]'
case_ invalid/alias-is-own-slug.json manifest true "alias-collision $W" '.items[1].aliases = ["tierlistify"]'
case_ invalid/unknown-tech.json manifest true "unknown-taxonomy $W" '.items[0].tech += ["rust"]'
case_ invalid/unknown-category.json manifest true "unknown-taxonomy $W" '.items[0].category = "games"'
case_ invalid/unknown-viewport.json manifest true "unknown-taxonomy $W" '.items[0].placements[2].config.viewport = "tablet"'
case_ invalid/duplicate-taxonomy.json manifest true "duplicate-taxonomy $W" '.taxonomy.tech += [{"slug": "react", "label": "React again"}]'
case_ invalid/undeclared-surface.json manifest true "unknown-surface $W" '.surfaces |= map(select(.id != "holodeck-dj-room"))'
case_ invalid/copy-undeclared-surface.json manifest true "unknown-surface $W" '.items[0].copy["saucedog"] = {"title": "x"}'
case_ invalid/unregistered-surface.json manifest true "unregistered-surface $W" '.surfaces += [{"id": "saucedog", "label": "saucedog.art"}]'
case_ invalid/duplicate-surface.json manifest true "duplicate-surface $W" '.surfaces += [.surfaces[0]]'
case_ invalid/placement-config.json manifest true "placement-config $W" '.items[0].placements[0].config = {"open": "sideways"}'
case_ invalid/placement-config-missing-required.json manifest true "placement-config $W" '.items[2].placements[0].config = {}'
case_ invalid/placement-key-missing.json manifest true "placement-key $W" 'del(.items[1].placements[0].key)'
case_ invalid/placement-key-duplicate.json manifest true "placement-key $W" '.items[1].placements[1].key = "desktop"'
case_ invalid/member-missing.json manifest true "member-missing $W" '.items[4].members += ["01JB7Z000000000000000000ZZ"]'
case_ invalid/member-self.json manifest true "member-self $W" ".items[4].members += [\"$(ID 4)\"]"
case_ invalid/member-cycle.json manifest true "member-cycle $W" ".items[4].members += [\"$(ID 5)\"] | .items[5].members += [\"$(ID 4)\"]"
case_ invalid/preview-not-owned.json manifest true "not-owned-domain $W" '.items[0].placements[1].config.preview.urls = ["https://videogamequest.me/"]'
case_ invalid/embed-not-owned.json manifest true "not-owned-domain $W" '.items[1].links[0].url = "https://tierlistify.vercel.app/"'
case_ invalid/owned-wildcard-shared-platform.json manifest true "shared-platform-wildcard $W" '.ownedDomains += ["*.netlify.app"]'
case_ invalid/bad-date-calendar.json manifest true "bad-date $W" '.items[2].date.value = "2025-02-30"'
case_ invalid/bad-experience-date.json manifest true "bad-date $W" '.profile.experience[0].end = "2023-02-29"'

# Public projection: only public items, no private fields.
# Members that aren't public are dropped too, or public collections would point at drafts.
PROJECT='[.items[] | select(.visibility == "public") | .id] as $pub
  | .items |= map(select(.visibility == "public") | del(.private)
      | if .members then .members |= map(select(. as $m | $pub | index($m))) else . end)'
case_ public/valid.json public-manifest true "$W" "$PROJECT"
case_ public/with-private.json public-manifest false schema "$PROJECT | .items[0].private = {\"notes\": \"leak\"}"
case_ public/with-draft.json public-manifest false schema "$PROJECT | .items[0].visibility = \"draft\""

printf '%s\n' "${EXP[@]}" | jq -s --indent 2 '{cases: .}' >expectations.json
echo "wrote ${#EXP[@]} cases"

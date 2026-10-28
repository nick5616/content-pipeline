# Decisions

Defaults chosen while planning, each one overturnable. Append new rows; don't
rewrite old ones. Full context: [plans/M0-M1.md](plans/M0-M1.md).

| # | Date | Decision |
|---|---|---|
| D1 | 2026-10-27 | Repo lives at `~/repos/content-pipeline` inside WSL Ubuntu 24.04. Build and run from WSL, never `/mnt/c`. |
| D2 | 2026-10-27 | Go module path `github.com/nick5616/canon`. |
| D3 | 2026-10-27 | Toolchain matches Toolbox: Go 1.27, Node 24 LTS (fnm), Task, `oklog/ulid/v2`, `modernc.org/sqlite`, Vitest. |
| D4 | 2026-10-27 | Contracts: JSON Schema 2020-12 is the source of truth; `santhosh-tekuri/jsonschema/v6` validates; `omissis/go-jsonschema` for Go types; `json-schema-to-zod` for TS; OpenAPI 3.1 for the local API; generated code committed with a CI drift check. |
| D5 | 2026-10-27 | Config is YAML at `$XDG_CONFIG_HOME/canon/config.yaml`; `canon.example.yaml` is committed. |
| D6 | 2026-10-27 | Working manifest, "pull every time": `base.json` + `working.json` in the state dir; remote generation checked before every manifest read; no merging in v1. |
| D7 | 2026-10-27 | `gs://artandmedia` (and now `gs://artandmedia-originals`) are import sources only. Canon never writes to them; config validation and provisioning both refuse them. |
| D8 | 2026-10-27 | Copy overrides may change `title`, `summary`, `why`, `date.label`, `richDescription` only. |
| D9 | 2026-10-27 | Imported items keep the old `content.ts` id as `slug` and in `aliases`. |
| D10 | 2026-10-27 | Owned domains on shared platforms are exact hosts; wildcards only on domains Nicole registers. |
| D11 | 2026-10-27 | `holodeck-art-room` feeds the gallery halls; visitor-submitted Art Studio stays out of Canon. |
| D12 | 2026-10-27 | Views carry `view`, `viewVersion`, `pipelineVersion`; no timestamps or manifest hash. |
| D13 | 2026-10-27 | `canon serve` listens on `127.0.0.1:4747`; token in the URL fragment, sent as `Authorization: Bearer`. |
| D14 | 2026-10-27 | Originals archive: a third bucket, PAP enforced, versioning on, no lifecycle deletes. Only the archive writer uploads unsanitized bytes, and only there. |
| D15 | 2026-10-27 | DJ room: `dj-crate/` becomes collection `music` ("Music") of `track` items on surface `holodeck-dj-room`. |
| D16 | 2026-10-27 | Public bucket CORS: GET and HEAD, `Content-Type`, for the site origins plus `localhost:5173`. Origins live in config (`publicCorsOrigins`). |
| D17 | 2026-10-28 | All three buckets get 30-day soft delete, including the archive (the plan listed it for "both buckets"; soft delete only adds protection). |
| D18 | 2026-10-28 | Provisioning reads config through `canon config env`, and the operator who gets TokenCreator is a config field (`operator: user:...`) rather than whatever account gcloud happens to be logged in as. |
| D19 | 2026-10-28 | Go types come from `github.com/atombender/go-jsonschema` (the `omissis` fork moved back; same project), run with `--only-models` so the validator alone decides validity. |
| D20 | 2026-10-28 | TypeScript 5.9 in `ui/` and `client/`, not 7.0: openapi-typescript needs the TS 5 compiler API, which the native TS 7 doesn't ship. |
| D21 | 2026-10-28 | Zod comes from json-schema-to-zod plus fixes in `client/scripts/gen.mjs` for what it drops (`if/then` without `else`, `not` on objects, `allOf` + `unevaluatedProperties`). A Vitest test proves Zod and Go give the same verdict on every fixture. |
| D22 | 2026-10-28 | View-only definitions live in `schema/view-common.schema.json`, not `common.schema.json`, so manifest codegen doesn't emit view types. `private` and `profile.links` are closed objects: open ones lose data through the generated Go types. |
| D23 | 2026-10-28 | Go view types are deferred to M5 (go-jsonschema resolves nested relative `$ref`s from the wrong directory). Go validates views by schema meanwhile. |
| D24 | 2026-10-28 | The art-portfolio view puts `section` on each entry instead of separate `sections.primary[]` / `sections.collections[]` lists, so the envelope's single `entries[]` holds everything. |
| D25 | 2026-10-28 | The public projection (M5) must also drop non-public ids from public collections' `members`; otherwise the public manifest points at drafts. Found by the public fixture. |

## Open for sign-off (M1 schemas)

The plan and design doc don't pin these down; each is a guess to confirm or change before the schemas freeze.

- `status` is a free slug for now (e.g. `operational`); make it an enum once the content.ts import shows every value.
- Asset roles: `thumbnail`, `screenshot`, `demo`, `original`, `cover`, `audio`, `video`, `document`.
- `credits: [{ name, role? }]` on items carries the track artist (D15) and any other credits.
- Facts beyond the doc: artwork `pixelWidth/pixelHeight/physical/palette`; photo `pixelWidth/pixelHeight/palette`; video `duration/pixelWidth/pixelHeight/fps`; writing `wordCount/publication`; experience `site`; collection has none. Track `key` is a free string until djgrid@v1 fixes its format.
- `profile.experience[]`: `organization, role, start, end?, location?, summary?, highlights?, url?`; `profile.education[]`: `institution, degree?, field?, start?, end?, summary?`.
- Views: desktop `open` is a flat `{type, route, url, newTab, window}`; software-portfolio entries add `thumbnail` and `richDescription {markdownUrl, component}`; DJ room has top-level `artist` and `tracks[]`, with `entries[]` holding the Music collection; `resolvedAsset` carries `alt`.
- `date.sortKey` is the integer `YYYYMMDD` with `00` for a missing month or day.

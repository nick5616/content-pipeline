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
| D26 | 2026-10-28 | M2 E1: HEIC is accepted. The sanitized HEIC is the source blob; M4 makes WebP derivatives with `heif-dec` (libheif, added to bootstrap and doctor). |
| D27 | 2026-10-28 | M2 E2: ingest stages the untouched original locally (`<cache>/originals/`); apply uploads it to the archive with an `archive-original` op, first in the plan. Replaces D14's "ingest uploads". |
| D28 | 2026-10-28 | M2 E3: `assetRef.original` records the SHA-256 of the original bytes. The public projection drops it and the public schema forbids it. |
| D29 | 2026-10-28 | M2 E4: a dropped image becomes `photo` if the original names a camera (Make/Model), otherwise `artwork`; video → `video`, audio → `track`, PDF → `writing`. Overridable. |
| D30 | 2026-10-28 | M2 E5: a draft's date is prefilled from the original's capture time (day precision), for review before publishing. |
| D31 | 2026-10-28 | TIFF needs explicit deletes: `exiftool -all=` leaves IFD0 Artist, ImageDescription, Make, Model and Software in TIFFs (there IFD0 is the image's own directory). The sanitizer deletes those IFD0 tags by name; the fail-closed verify caught it. |
| D32 | 2026-10-28 | PNGs are sanitized in Go with a chunk allowlist, not exiftool (which leaves unreadable zTXt raw profiles, GPS included). Orientation stored in a PNG eXIf chunk is dropped with it; browsers rarely honor it anyway. |
| D33 | 2026-10-28 | Local API 1.1.0 (additive): IngestBatch files gain `note` (why nothing changed), and OpType gains `archive-original` (D27). |
| D34 | 2026-10-28 | M3 archives every staged original in `<cache>/originals/`, not only the ones a manifest asset references: two originals that differ only in metadata (say, GPS) sanitize to one blob, and both should reach the archive. |
| D35 | 2026-10-28 | The `heif-dec` tool also resolves as `heif-convert`, its name before libheif 1.18 (Ubuntu 24.04 ships 1.17.6). Ubuntu splits HEVC out of `libheif-examples`: `libheif-plugin-libde265` (decode, needed by M4) and `libheif-plugin-x265` (encode, test fixtures only); bootstrap and CI install both. The allowlist gains `Meta:PrimaryItemReference`, the only structural tag a real sanitized HEIC added. |
| D36 | 2026-10-28 | M3 F1: `ls`, `show`, `item new/set/rm` and `taxonomy ls/set` (CLI and API) land in M3, as thin layers over `store.Update`. |
| D37 | 2026-10-28 | M3 F2: a plan records the working manifest's revision; apply refuses if it changed. Plans don't embed the manifest. |
| D38 | 2026-10-28 | M3 F3: a content-addressed object with missing or wrong `sha256` metadata is re-uploaded in the private bucket (versioning keeps the old one) and is a plan error in the archive. |
| D39 | 2026-10-28 | M3 F4: local API 1.2.0 (additive): `GET /api/plans/{id}/apply`, `Plan.revision`, `summary.archive`, OpType `write-history`, and a 409 on `createPlan` when local and remote both moved. SSE stays with M6. |
| D40 | 2026-10-28 | `item rm` refuses an item that is public in base (the remote manifest as last pulled or applied); anything else, including a draft that's already uploaded, can be removed. Its blobs then show as orphans until M8's gc. |
| D41 | 2026-10-28 | `canon apply <plan>` doesn't prompt: the saved plan is the review. History snapshots are named with the time the plan was made, so a plan's paths are fixed before apply runs. |
| D42 | 2026-10-29 | Real data is worked on through private copies: `gs://artandmedia-copy` and `gs://artandmedia-originals-copy` (project `artandmedia`, US, PAP enforced, versioning on, 30-day soft delete), copied byte-for-byte and checked by name, size, MD5 and CRC32C. Imports read the copies; config and provisioning refuse all four buckets as Canon buckets. |
| D43 | 2026-10-29 | `canon import bucket gs://<bucket>[/<prefix>]` lands early (M7 step 2). It only lists and reads the source, and runs each file through ingest, so originals reach the archive and only sanitized bytes upload. Every image becomes an `artwork` (the plan's "one artwork per object", even with camera EXIF); audio becomes a `track`. Mapping: `art-name` → title, `art-description` → summary, `art-date` then `created` → date (wording such as "Circa 2022" kept as the label; art-date wins a conflict, which is flagged), `private=true` → a private note to keep it unpublished, `art-order` → order in the folder's collection. Each top-level folder becomes a draft collection whose slug is the folder name (the holodeck's collection id), except `dj-crate` → `music` (D15). In `dj-crate/`, `crate.json` gives each track its title, artist credit and beat grid until `djgrid@v1` (M4); the per-track analysis JSON is skipped. Metadata is applied only to drafts the import creates, so reruns keep edits; the private marker is the exception and is also added to an existing item. The report (`<stateDir>/imports/<bucket>.json`) maps each object to its item; M5 turns it into old-URL-to-new-URL redirects once public URLs exist. |
| D44 | 2026-10-29 | The verify allowlist gains what survives `-all=` in an iPhone HEIC with an HDR gain map: `Meta:MetaImageSize`, `QuickTime:CleanAperture` (the gain map's crop), `XMP-HDRGainMap:HDRGainMapVersion` and `XMP-x:XMPToolkit` (the gain map's own XMP item: a format version and the XMP library version). The main XMP, with the capture date and iOS version, is still removed. Found by `paintings/cherry-blossoms.HEIC`. |
| D45 | 2026-10-29 | M4 G1: jobs run locally; `canon plan` runs pending jobs first (`--no-jobs` skips), and `canon serve` runs them after ingest. Apply only uploads finished outputs (`run-job` ops), so the manifest apply writes is still the planned one (D37). |
| D46 | 2026-10-29 | M4 G2: images are resized and encoded with libvips (`vips`), which converts to sRGB from the embedded profile and reads HEIC. Added to bootstrap and doctor. |
| D47 | 2026-10-29 | M4 G3: `thumb@v1` widths are 320, 800, 1600, 2048 and 3200, never upscaled (the source width replaces any wider one), WebP quality 80. `lqip@v1` replaces the holodeck's 48 px preview. |
| D48 | 2026-10-29 | M4 G4: recipes only fill empty facts; `canon jobs run --refresh-facts` overwrites. Imported `crate.json` values (from the AIFF masters) stay. |
| D49 | 2026-10-29 | M4 G5: `dj-mp3@v1` runs only on lossless sources (AIFF, WAV, FLAC); an MP3 source is played as is. |
| D50 | 2026-10-29 | M4 G6: local API 1.3.0 (additive): `POST /api/jobs/run` (`runJobs`, CLI `canon jobs run`). `run-job` ops mean "upload this job's outputs". |
| D51 | 2026-10-29 | M5 H1: a public item exposes only what its views link: `thumb` sizes (at most 3200 px) and `lqip` for images, the source MP3 (or `dj-mp3`), `djgrid` analysis and previews for tracks, `web` and poster for video. Full-resolution sources stay private. |
| D52 | 2026-10-29 | M5 H2: the public bucket has no object versioning (soft delete stays at 30 days), so an unpublished file can't be read by generation number. It's a projection apply can rebuild from the private bucket. |
| D53 | 2026-10-29 | M5 H3: public `blobs/` and `derived/` are served `public, max-age=86400`, not a year, so an unpublished file leaves caches within a day. Manifest and views stay at 60 s. |
| D54 | 2026-10-29 | M5 H4: public URLs are `publicBaseUrl` + path; the default is `https://storage.googleapis.com/<publicBucket>`. |
| D55 | 2026-10-29 | M5 H5: nothing is published automatically. M5 ends with `canon view` previews; placing and publishing the art and DJ rooms is a separate, approved step. |
| D56 | 2026-10-29 | `thumb` and `poster` are v2: receipts record each image's width and height (read from the verified file), which views need. The 121 `thumb@v1` outputs in the private bucket are orphans until M8's gc. |
| D57 | 2026-10-29 | The DJ room's crate `artist` is the Music collection's credit, else the tracks' most common artist credit, else the profile name. (The plan said collection credit, then profile name; your tracks are credited to Bird Dog and the collection to no one.) |
| D58 | 2026-10-30 | Local API 1.4.0 (additive): `PUT /api/surfaces/{id}` (`putSurface`, CLI `canon surface set <id> label=...`). A manifest made from scratch declares no surfaces, and placements can only name declared ones. |
| D59 | 2026-10-30 | M7 J5: owned domains are `nicolebelovoskey.com`, `nickbelovoskey.com`, `nickeltools.dev`, `saucedog.art`, each with `*.` subdomains, plus each `netlify.app` host the content links to (exact). `videogamequest.me` is gone (links rewrite to `rpg-quests.netlify.app`). MidiMuse and Smart Piano stay separate items; Smart Piano ranks after MidiMuse (it's the lightweight toy of the same music math). Categories merge: Games and Gaming become `gaming` ("Gaming"); Social Tools and Social App become `social` ("Social"). |
| D60 | 2026-10-30 | M7 J2: sites read `views/<surface>.json` from the public bucket with a plain `fetch` and a hand-written TypeScript types file per view; no vendored or published client. |
| D61 | 2026-10-30 | `tierlistify.com` was given up: Tierlistify's links and previews use `tierlistify.netlify.app` (a rewrite rule, like `videogamequest.me`). `friendex.online` (and subdomains) is owned. |

## M1 schema calls (approved 2026-10-28)

The plan and design doc didn't pin these down. Approved as written ("yes to all", 2026-10-28); changing them now is a versioned change (`schemaVersion` / `viewVersion`).

- `status` is a free slug for now (e.g. `operational`); make it an enum once the content.ts import shows every value.
- Asset roles: `thumbnail`, `screenshot`, `demo`, `original`, `cover`, `audio`, `video`, `document`.
- `credits: [{ name, role? }]` on items carries the track artist (D15) and any other credits.
- Facts beyond the doc: artwork `pixelWidth/pixelHeight/physical/palette`; photo `pixelWidth/pixelHeight/palette`; video `duration/pixelWidth/pixelHeight/fps`; writing `wordCount/publication`; experience `site`; collection has none. Track `key` is a free string until djgrid@v1 fixes its format.
- `profile.experience[]`: `organization, role, start, end?, location?, summary?, highlights?, url?`; `profile.education[]`: `institution, degree?, field?, start?, end?, summary?`.
- Views: desktop `open` is a flat `{type, route, url, newTab, window}`; software-portfolio entries add `thumbnail` and `richDescription {markdownUrl, component}`; DJ room has top-level `artist` and `tracks[]`, with `entries[]` holding the Music collection; `resolvedAsset` carries `alt`.
- `date.sortKey` is the integer `YYYYMMDD` with `00` for a missing month or day.

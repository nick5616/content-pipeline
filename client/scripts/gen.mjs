// Generates Zod schemas in src/gen/ from the JSON Schemas in ../schema.
// Run by `task gen` (via schema/gen.go); CI fails if the output drifts.
//
// json-schema-to-zod does the conversion. Three things it doesn't handle are
// fixed here, so Zod accepts and rejects exactly what the Go validator does
// for the schema-level rules:
//   - if/then without else, and its Zod 3-only error handling: emitted as a
//     superRefine that runs the generated branch schemas.
//   - `not` on an object (no members on a project, no private in public).
//   - allOf of plain objects with unevaluatedProperties: false (view entries):
//     merged into one strict object before conversion.
import $RefParser from "@apidevtools/json-schema-ref-parser";
import { jsonSchemaToZod, parseSchema } from "json-schema-to-zod";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const schemaDir = join(here, "..", "..", "schema");
const outDir = join(here, "..", "src", "gen");

export const SURFACES = [
  "nickel-desktop",
  "software-portfolio",
  "design-portfolio",
  "art-portfolio",
  "holodeck-showroom",
  "holodeck-art-room",
  "holodeck-dj-room",
];

const pascal = (s) => s.replace(/(^|-)([a-z])/g, (_, __, c) => c.toUpperCase());

const targets = [
  { src: "manifest.schema.json", out: "manifest.ts", name: "Manifest" },
  { src: "public-manifest.schema.json", out: "public-manifest.ts", name: "PublicManifest" },
  ...SURFACES.map((s) => ({
    src: `surfaces/${s}/view.schema.json`,
    out: `views/${s}.ts`,
    name: `${pascal(s)}View`,
  })),
];

const isPlainObjectSchema = (s) =>
  s && typeof s === "object" && !("if" in s) && !("not" in s) && !("anyOf" in s) && !("oneOf" in s) &&
  (s.type === "object" || "properties" in s);

// Merge allOf members that are plain object schemas into their parent. A
// property defined on both sides keeps both constraints (allOf of the two).
function flatten(node, seen = new WeakMap()) {
  if (Array.isArray(node)) return node.map((n) => flatten(n, seen));
  if (!node || typeof node !== "object") return node;
  if (seen.has(node)) return seen.get(node);
  const out = {};
  seen.set(node, out);
  for (const [k, v] of Object.entries(node)) out[k] = flatten(v, seen);

  if (Array.isArray(out.allOf)) {
    const keep = [];
    for (const member of out.allOf) {
      if (!isPlainObjectSchema(member) || member.additionalProperties === false) {
        keep.push(member);
        continue;
      }
      out.type ??= "object";
      out.properties ??= {};
      for (const [p, ps] of Object.entries(member.properties ?? {})) {
        out.properties[p] = p in out.properties ? { allOf: [ps, out.properties[p]] } : ps;
      }
      out.required = [...new Set([...(member.required ?? []), ...(out.required ?? [])])];
      for (const k of ["description", "title"]) if (member[k] && !out[k]) out[k] = member[k];
    }
    if (keep.length) out.allOf = keep;
    else delete out.allOf;
  }
  // `{ properties: ... }` with no type converts to z.any(). Every such
  // subschema here applies to an object, so saying so is equivalent.
  if (out.properties && !out.type) out.type = "object";
  if (out.unevaluatedProperties === false && !out.allOf) {
    delete out.unevaluatedProperties;
    out.additionalProperties = false;
  }
  return out;
}

const child = (refs, ...path) => ({ ...refs, path: [...refs.path, ...path] });

// Zod code that adds every issue from parsing `value` with `schemaCode`.
const forward = (schemaCode) => `{ const r = ${schemaCode}.safeParse(value); if (!r.success) for (const i of r.error.issues) ctx.addIssue({ code: "custom", message: i.message, path: i.path, input: value }); }`;

function conditional(s, refs) {
  const $if = parseSchema(s.if, child(refs, "if"));
  const $then = s.then ? parseSchema(s.then, child(refs, "then")) : null;
  const $else = s.else ? parseSchema(s.else, child(refs, "else")) : null;
  return `if (${$if}.safeParse(value).success) ${$then ? forward($then) : "{}"} else ${$else ? forward($else) : "{}"}`;
}

// `{ required: [...] }` alone: the keys are present (not merely undefined).
const isRequiredOnly = (s) =>
  Array.isArray(s.required) && Object.keys(s).every((k) => k === "required" || (k === "type" && s.type === "object"));

function override(s, refs) {
  if (!s || typeof s !== "object") return undefined;
  if (isRequiredOnly(s)) {
    return `z.custom((v) => typeof v === "object" && v !== null && !Array.isArray(v) && ${JSON.stringify(s.required)}.every((k) => Object.hasOwn(v, k)), { message: ${JSON.stringify("missing required: " + s.required.join(", "))} })`;
  }
  const conds = (s.allOf ?? []).filter((m) => m && typeof m === "object" && "if" in m);
  const hasNot = "not" in s && (s.type === "object" || "properties" in s || "allOf" in s);
  if (!("if" in s) && conds.length === 0 && !hasNot) return undefined;

  const base = { ...s };
  delete base.if;
  delete base.then;
  delete base.else;
  const rest = (s.allOf ?? []).filter((m) => !conds.includes(m));
  if (rest.length) base.allOf = rest;
  else delete base.allOf;
  if (hasNot) delete base.not;
  const hasBase = Object.keys(base).some((k) => !["description", "title", "$schema"].includes(k));

  const checks = [];
  if ("if" in s) checks.push(conditional(s, refs));
  conds.forEach((c, i) => checks.push(conditional(c, child(refs, "allOf", i))));
  if (hasNot) {
    const n = parseSchema(s.not, child(refs, "not"));
    checks.push(`if (${n}.safeParse(value).success) ctx.addIssue({ code: "custom", message: ${JSON.stringify("must not match: " + JSON.stringify(s.not))}, input: value });`);
  }
  // The checks see the raw input: a refinement on the base would see its
  // output, after z.object has stripped unknown keys like `private`.
  // (z.intersection would also work for that, but in Zod 4 it drops the
  // base's unrecognized-key errors.)
  const body = `(value, ctx) => {\n${checks.join("\n")}\n`;
  return hasBase
    ? `z.preprocess(${body}return value;\n}, ${parseSchema(base, refs, true)})`
    : `z.any().superRefine(${body}})`;
}

const header = (src) =>
  `// Code generated by client/scripts/gen.mjs from schema/${src}; DO NOT EDIT.\n// Run \`task gen\` to regenerate.\n\n`;

for (const t of targets) {
  const deref = await $RefParser.dereference(join(schemaDir, t.src), {
    resolve: { external: true, http: false },
    dereference: { circular: false },
  });
  const flat = flatten(deref);
  let code = jsonSchemaToZod(flat, {
    module: "esm",
    name: `${t.name}Schema`,
    type: t.name,
    parserOverride: override,
    withoutDefaults: true,
  });
  code = header(t.src) + "/* eslint-disable */\n" + code.trimEnd() + "\n";
  const out = join(outDir, t.out);
  await mkdir(dirname(out), { recursive: true });
  await writeFile(out, code);
}

const index =
  header("surfaces/*/view.schema.json") +
  SURFACES.map((s) => `import { ${pascal(s)}ViewSchema } from "./views/${s}.js";`).join("\n") +
  "\n\n" +
  SURFACES.map((s) => `export type { ${pascal(s)}View } from "./views/${s}.js";`).join("\n") +
  "\n\n/** Every registered surface and the Zod schema for its view. */\nexport const viewSchemas = {\n" +
  SURFACES.map((s) => `  "${s}": ${pascal(s)}ViewSchema,`).join("\n") +
  "\n} as const;\n";
await writeFile(join(outDir, "views.ts"), index);
console.log(`generated ${targets.length} schemas`);

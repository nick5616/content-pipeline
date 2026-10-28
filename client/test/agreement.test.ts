// Go and TS agree: the same fixtures, the same schema verdicts. Go checks the
// same expectations.json in engine/manifest; semantic codes are Go-only.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ManifestSchema } from "../src/gen/manifest.js";
import { PublicManifestSchema } from "../src/gen/public-manifest.js";
import { viewSchemas } from "../src/gen/views.js";

const testdata = join(import.meta.dirname, "..", "..", "schema", "testdata");
const read = (p: string): unknown => JSON.parse(readFileSync(join(testdata, p), "utf8"));

type Case = { file: string; schema: string; schemaValid: boolean; codes: string[] };
const { cases } = read("expectations.json") as { cases: Case[] };
const schemas = { manifest: ManifestSchema, "public-manifest": PublicManifestSchema } as const;

describe("manifest fixtures", () => {
  it("has the cases", () => expect(cases.length).toBeGreaterThan(40));
  for (const c of cases) {
    it(`${c.file} is ${c.schemaValid ? "valid" : "invalid"}`, () => {
      const r = schemas[c.schema as keyof typeof schemas].safeParse(read(c.file));
      expect(r.success, r.success ? "" : JSON.stringify(r.error.issues.slice(0, 3))).toBe(c.schemaValid);
    });
  }
});

describe("golden views", () => {
  for (const [surface, schema] of Object.entries(viewSchemas)) {
    it(`${surface} validates`, () => {
      const r = schema.safeParse(read(`views/${surface}.json`));
      expect(r.success, r.success ? "" : JSON.stringify(r.error.issues.slice(0, 3))).toBe(true);
    });
    it(`${surface} rejects an extra entry field`, () => {
      const v = read(`views/${surface}.json`) as { entries: Record<string, unknown>[] };
      v.entries[0]!.private = { notes: "leak" };
      expect(schema.safeParse(v).success).toBe(false);
    });
  }
});

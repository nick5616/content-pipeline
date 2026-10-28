import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { loadView, loadViewFromFile, parseView, ViewError } from "../src/index.js";

const goldenPath = (s: string) => join(import.meta.dirname, "..", "..", "schema", "testdata", "views", `${s}.json`);
const golden = (s: string) => JSON.parse(readFileSync(goldenPath(s), "utf8"));

function fakeFetch(body: unknown, status = 200): typeof fetch {
  return (async (url: string | URL | Request) => {
    fakeFetch.lastUrl = String(url);
    return new Response(typeof body === "string" ? body : JSON.stringify(body), { status });
  }) as typeof fetch;
}
fakeFetch.lastUrl = "";

describe("loadView", () => {
  it("accepts the golden view and types it", async () => {
    const v = await loadView("holodeck-dj-room", {
      baseUrl: "https://storage.googleapis.com/canon-public-example/",
      fetch: fakeFetch(golden("holodeck-dj-room")),
    });
    expect(fakeFetch.lastUrl).toBe("https://storage.googleapis.com/canon-public-example/views/holodeck-dj-room.json");
    expect(v.tracks[0]?.camelot).toBe("8A");
  });

  it("rejects a wrong viewVersion with a readable error", async () => {
    const bad = { ...golden("software-portfolio"), viewVersion: 2 };
    await expect(loadView("software-portfolio", { baseUrl: "https://x", fetch: fakeFetch(bad) })).rejects.toThrow(
      /software-portfolio: viewVersion 2 is not 1; upgrade @canon\/client/,
    );
  });

  it("rejects a missing field and names it", async () => {
    const bad = golden("holodeck-showroom");
    delete bad.entries[0].title;
    const err = await loadView("holodeck-showroom", { baseUrl: "https://x", fetch: fakeFetch(bad) }).catch((e) => e);
    expect(err).toBeInstanceOf(ViewError);
    expect(err.message).toMatch(/does not match its schema/);
    expect(err.message).toMatch(/entries\.0\.title/);
  });

  it("rejects another surface's view", () => {
    expect(() => parseView("art-portfolio", golden("design-portfolio"))).toThrow(/got the view for "design-portfolio"/);
  });

  it("surfaces HTTP and JSON errors", async () => {
    await expect(loadView("art-portfolio", { baseUrl: "https://x", fetch: fakeFetch("nope", 404) })).rejects.toThrow(/404/);
    await expect(loadView("art-portfolio", { baseUrl: "https://x", fetch: fakeFetch("<html>") })).rejects.toThrow(/not JSON/);
  });

  it("refuses unregistered surfaces", () => {
    expect(() => parseView("saucedog" as never, {})).toThrow(/not a registered surface/);
  });
});

describe("loadViewFromFile", () => {
  it("reads and validates for build-time baking", async () => {
    const v = await loadViewFromFile("art-portfolio", goldenPath("art-portfolio"));
    expect(v.entries.map((e) => e.section)).toEqual(["primary", "collections"]);
  });
});

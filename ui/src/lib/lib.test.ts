import { describe, expect, it } from "vitest";
import { href, parseRoute } from "./route";
import { localize } from "./images";
import { streamEvents } from "./events";
import { defaultConfig, PLACEMENT_SCHEMAS } from "../forms/PlacementForm";

describe("routes", () => {
  it("round-trips", () => {
    for (const r of [{ screen: "library" }, { screen: "item", id: "rpg-quests" }, { screen: "plan" }, { screen: "surfaces", id: "nickel-desktop" }] as const)
      expect(parseRoute(href(r))).toEqual(r);
    expect(parseRoute("#/nonsense")).toEqual({ screen: "library" });
  });
});

describe("localize", () => {
  it("maps public URLs to the local cache", () => {
    const k = "a".repeat(64);
    expect(localize(`https://storage.googleapis.com/canon-public/derived/${k}/w320.webp`)).toBe(`/api/local/derived/${k}/w320.webp`);
    expect(localize(`https://storage.googleapis.com/canon-public/blobs/sha256/aa/aa/${k}.mp3`)).toBe(`/api/local/blobs/${k}`);
    expect(localize("https://example.com/x.png")).toBeNull();
  });
});

describe("events", () => {
  it("parses data lines across chunk boundaries", async () => {
    const enc = new TextEncoder();
    const chunks = [': hi\n\nevent: job\ndata: {"type":"job","id":"1","da', 'ta":{}}\n\ndata: not json\n\ndata: {"type":"sync","id":"sync","data":{}}\n\n'];
    const ctl = new AbortController();
    const got: string[] = [];
    const fetchImpl = (async () =>
      new Response(
        new ReadableStream({
          start(c) {
            chunks.forEach((x) => c.enqueue(enc.encode(x)));
            c.close();
          },
        }),
      )) as unknown as typeof fetch;
    const done = streamEvents("t", (e) => {
      got.push(e.type);
      if (got.length === 2) ctl.abort();
    }, ctl.signal, () => {}, fetchImpl);
    await done;
    expect(got).toEqual(["job", "sync"]);
  });
});

describe("placement schemas", () => {
  it("are all loaded, with defaults for their required fields", () => {
    expect(Object.keys(PLACEMENT_SCHEMAS).sort()).toEqual([
      "art-portfolio",
      "design-portfolio",
      "holodeck-art-room",
      "holodeck-dj-room",
      "holodeck-showroom",
      "nickel-desktop",
      "software-portfolio",
    ]);
    expect(defaultConfig("nickel-desktop")).toEqual({ open: "new-tab" });
    expect(defaultConfig("art-portfolio")).toEqual({ section: "primary" });
    expect(defaultConfig("holodeck-dj-room")).toEqual({});
  });
});

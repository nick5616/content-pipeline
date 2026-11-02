import { act, render, screen, waitFor, within, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { fakeApi } from "../test/fakeApi";
import { parseRoute } from "../lib/route";
import type { Plan } from "../lib/types";

function start(hash = "#/", api = fakeApi()) {
  window.location.hash = hash;
  render(<App token="t0k" fetchImpl={api.fetchImpl} />);
  return api;
}

beforeEach(() => {
  window.location.hash = "";
});

describe("App", () => {
  it("asks for the token when there is none", () => {
    render(<App />);
    expect(screen.getByText(/No token/)).toBeTruthy();
  });

  it("sends the bearer token on every call", async () => {
    const api = start();
    await screen.findByText("Night Market");
    expect(api.calls.length).toBeGreaterThan(2);
    expect(api.calls.every((c) => c.headers.get("Authorization") === "Bearer t0k")).toBe(true);
  });
});

describe("Library", () => {
  it("lists, filters and searches", async () => {
    start();
    await screen.findByText("Night Market");
    expect(screen.getByText("Fog Bridge")).toBeTruthy();
    expect(screen.getByText(/2 of 2 · 1 drafts/)).toBeTruthy();
    await userEvent.selectOptions(screen.getByLabelText("Visibility"), "draft");
    expect(screen.queryByText("Night Market")).toBeNull();
    await userEvent.selectOptions(screen.getByLabelText("Visibility"), "");
    await userEvent.type(screen.getByLabelText("Search"), "night");
    expect(screen.queryByText("Fog Bridge")).toBeNull();
  });

  it("moves with j and k and edits with e", async () => {
    start();
    await screen.findByText("Night Market");
    // Sorted by title: Fog Bridge, then Night Market.
    fireEvent.keyDown(window, { key: "j" });
    fireEvent.keyDown(window, { key: "e" });
    await waitFor(() => expect(parseRoute(window.location.hash)).toEqual({ screen: "item", id: "01JB7Z00000000000000000001" }));
  });
});

describe("Item editor", () => {
  it("saves a field as a merge patch with If-Match", async () => {
    const api = start("#/items/night-market");
    const title = await screen.findByDisplayValue("Night Market");
    await userEvent.clear(title);
    await userEvent.type(title, "Lanterns");
    await screen.findByText("Saved to the working manifest", {}, { timeout: 3000 });
    const patch = api.calls.filter((c) => c.method === "PATCH").at(-1)!;
    expect(patch.body).toEqual({ title: "Lanterns" });
    expect(patch.headers.get("If-Match")).toBe('"rev1"');
    expect(patch.headers.get("Content-Type")).toBe("application/merge-patch+json");
    expect(api.state.items[0]!.title).toBe("Lanterns");
  });

  it("reapplies the change when the manifest moved underneath", async () => {
    const api = start("#/items/night-market");
    const summary = (await screen.findAllByRole("textbox"))[1]!;
    await screen.findByDisplayValue("Night Market");
    api.state.revision = "moved-by-the-cli";
    await userEvent.type(summary, "Hello");
    await screen.findByText(/changed elsewhere/, {}, { timeout: 3000 });
    await screen.findByText("Saved to the working manifest", {}, { timeout: 3000 });
    expect(api.state.items[0]!.summary).toBe("Hello");
  });

  it("shows validation problems and saves nothing", async () => {
    const api = fakeApi({
      "PATCH /api/items/:id": () =>
        api.json(
          { title: "Invalid", detail: "the change would make the manifest invalid", problems: [{ code: "schema", severity: "error", path: "/items/0/title", message: "too long" }] },
          422,
        ),
    });
    start("#/items/night-market", api);
    const title = await screen.findByDisplayValue("Night Market");
    await userEvent.type(title, "!");
    await screen.findByText(/Not saved/, {}, { timeout: 3000 });
    expect(screen.getByText("too long")).toBeTruthy();
    expect(api.state.items[0]!.title).toBe("Night Market");
  });

  it("toggles visibility at once", async () => {
    const api = start("#/items/fog-bridge");
    const box = await screen.findByLabelText("Public");
    await userEvent.click(box);
    await waitFor(() => expect(api.state.items[1]!.visibility).toBe("public"));
  });
});

const plan: Plan = {
  id: "01JB7Z0000000000000000PLAN",
  generation: 41,
  revision: "rev1",
  summary: { archive: 0, upload: 1, jobs: 0, publish: 1, unpublish: 0, orphaned: 1 },
  ops: [
    { type: "upload-blob", path: "blobs/sha256/aa/aa/" + "a".repeat(64) + ".jpg", bucket: "private", item: "01JB7Z00000000000000000001", detail: "night-market", bytes: 2048 },
    { type: "publish-copy", path: "derived/" + "b".repeat(64) + "/w320.webp", bucket: "public", item: null, detail: "holodeck-art-room", bytes: 900 },
    { type: "orphan", path: "blobs/sha256/ff/ff/" + "f".repeat(64) + ".gif", bucket: "private", item: null, detail: "not referenced", bytes: 10 },
  ],
  problems: [],
};

describe("Plan review", () => {
  it("makes a plan, applies exactly it, and follows progress", async () => {
    const api = fakeApi({
      "POST /api/plans": () => api.json(plan, 201),
      "POST /api/plans/:id/apply": () => api.json({ plan: plan.id, state: "running", done: 0, total: 2, newGeneration: null, error: null }, 202),
    });
    start("#/plan", api);
    await userEvent.click(await screen.findByText("Make plan"));
    await screen.findByText("Upload to the private bucket");
    expect(screen.getByText("Publish")).toBeTruthy();
    expect(screen.getByText(/Orphans/)).toBeTruthy();
    await userEvent.click(screen.getByText("Apply 2 changes"));
    expect(api.calls.some((c) => c.method === "POST" && c.path === `/api/plans/${plan.id}/apply`)).toBe(true);
    await screen.findByText("0 of 2");
    act(() => api.emit({ type: "apply", id: plan.id, data: { plan: plan.id, state: "done", done: 2, total: 2, newGeneration: 42, error: null } }));
    await screen.findByText(/now generation 42/, {}, { timeout: 3000 });
  });

  it("says when a plan is stale", async () => {
    const api = fakeApi({
      "POST /api/plans": () => api.json(plan, 201),
      "POST /api/plans/:id/apply": () => api.json({ title: "Conflict", detail: "the working manifest was edited since this plan was made." }, 409),
    });
    start("#/plan", api);
    await userEvent.click(await screen.findByText("Make plan"));
    await userEvent.click(await screen.findByText("Apply 2 changes"));
    await screen.findByText(/Make the plan again/);
  });

  it("says when there is nothing to do", async () => {
    const api = fakeApi({ "POST /api/plans": () => api.json({ ...plan, ops: [], summary: { ...plan.summary, upload: 0, publish: 0, orphaned: 0 } }, 201) });
    start("#/plan", api);
    await userEvent.click(await screen.findByText("Make plan"));
    await screen.findByText(/No changes/);
  });
});

describe("Plan diffs", () => {
  it("expands a view's diff against the bucket", async () => {
    const withView: Plan = {
      ...plan,
      ops: [...plan.ops, { type: "write-view", path: "views/nickel-desktop.json", bucket: "public", item: null, detail: "nickel-desktop", bytes: 300 }],
    };
    const api = fakeApi({
      "POST /api/plans": () => api.json(withView, 201),
      "GET /api/plans/:id/diff": (_c, u) =>
        u.searchParams.get("path") === "views/nickel-desktop.json"
          ? api.json({
              path: "views/nickel-desktop.json",
              bucket: "public",
              exists: true,
              added: 1,
              removed: 1,
              rough: false,
              hunks: [
                {
                  oldStart: 10,
                  oldLines: 3,
                  newStart: 10,
                  newLines: 3,
                  lines: [
                    { op: " ", text: '  "slug": "boards",' },
                    { op: "-", text: '  "title": "Boards",' },
                    { op: "+", text: '  "title": "Story Boards",' },
                  ],
                },
              ],
            })
          : api.json({ title: "Nothing to diff" }, 404),
    });
    start("#/plan", api);
    await userEvent.click(await screen.findByText("Make plan"));
    await userEvent.click(await screen.findByText("Show diff"));
    await screen.findByText(/"title": "Story Boards"/);
    expect(screen.getByText("+1")).toBeTruthy();
    expect(screen.getByText(/"title": "Boards"/).closest("tr")!.className).toBe("remove");
    // Line numbers: the removed line is old 11, the added one new 11.
    expect(screen.getByText(/@@ −10,3 \+10,3 @@/)).toBeTruthy();
    await userEvent.click(screen.getByText("Hide diff"));
    expect(screen.queryByText(/"title": "Story Boards"/)).toBeNull();
  });
});

describe("Ingest", () => {
  it("uploads dropped files and follows each one", async () => {
    const batch = { id: "B1", files: [{ name: "a.png", size: 3, detectedType: null, stage: "queued", blob: null, item: null, error: null, note: null }] };
    const api = fakeApi({ "POST /api/ingest": () => api.json(batch, 202) });
    start("#/ingest", api);
    const input = (await screen.findByText("Drop files here")).closest("label")!.querySelector("input")!;
    // The input is hidden behind its label (a real click opens the picker);
    // fire its change directly.
    fireEvent.change(input, { target: { files: [new File(["png"], "a.png", { type: "image/png" })] } });
    await waitFor(() => expect(api.calls.map((c) => c.method + " " + c.path)).toContain("POST /api/ingest"));
    await screen.findByText("a.png");
    const post = api.calls.find((c) => c.method === "POST" && c.path === "/api/ingest")!;
    expect((post.body as FormData).getAll("files")).toHaveLength(1);
    act(() =>
      api.emit({ type: "ingest", id: "B1", data: { ...batch, files: [{ ...batch.files[0]!, stage: "failed", error: "verify: GPS survived" }] } }),
    );
    await screen.findByText("verify: GPS survived", {}, { timeout: 3000 });
  });
});

describe("Jobs", () => {
  it("lists failures with the tool's output and retries them", async () => {
    const job = { jobkey: "c".repeat(64), recipe: "thumb@v2", source: "sha256:" + "a".repeat(64), item: null, state: "failed", attempts: 1, lastError: "vips: no such file" };
    const api = fakeApi({
      "GET /api/jobs": () => api.json([job]),
      "POST /api/jobs/:id/retry": () => api.json({ ...job, state: "pending" }),
    });
    start("#/jobs", api);
    await screen.findByText("vips: no such file");
    await userEvent.click(screen.getByText("Retry"));
    expect(api.calls.some((c) => c.method === "POST" && c.path === `/api/jobs/${job.jobkey}/retry`)).toBe(true);
  });
});

describe("Surface preview", () => {
  it("sketches the art room and lists what changes", async () => {
    const image = { url: "https://x/derived/" + "d".repeat(64) + "/w320.webp", width: 320, height: 240, srcset: [{ url: "https://x/derived/" + "d".repeat(64) + "/w320.webp", width: 320 }], lqip: null, alt: null };
    const view = {
      view: "holodeck-art-room",
      viewVersion: 1,
      pipelineVersion: "0.5.0",
      taxonomy: { categories: [], tech: [], mediums: [], tags: [] },
      entries: [{ id: "H", slug: "sketches", key: null, title: "Sketches", summary: "Quick ones.", pieces: [{ id: "P", slug: "p", title: "Lion", description: null, date: null, image }] }],
    };
    const api = fakeApi({
      "GET /api/surfaces/:id/preview": () => api.json(view),
      "GET /api/surfaces/:id/changes": () =>
        api.json({ surface: "holodeck-art-room", live: true, added: [], removed: [], changed: [{ id: "H", key: null, slug: "sketches", title: "Sketches", fields: ["summary"] }], header: false }),
    });
    start("#/surfaces/holodeck-art-room", api);
    await screen.findByText("Quick ones.");
    expect(screen.getByText("Lion")).toBeTruthy();
    const ch = screen.getByText("Changes since the live view").parentElement!;
    expect(within(ch).getByText(/Sketches/)).toBeTruthy();
  });
});

describe("Doctor", () => {
  it("shows the checks", async () => {
    start("#/doctor");
    await screen.findByText("ffmpeg");
  });
});

import type { CanonEvent, Item, SurfaceInfo, Taxonomy } from "../lib/types";

export type Call = { method: string; path: string; headers: Headers; body: unknown };
type Handler = (call: Call, url: URL) => Response | Promise<Response>;

const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });

export const taxonomy: Taxonomy = {
  categories: [{ slug: "art", label: "Art", icon: "🎨" }],
  tech: [{ slug: "typescript", label: "TypeScript" }],
  mediums: [],
  tags: [{ slug: "ink", label: "Ink" }],
  viewports: [],
};

export function makeItems(): Item[] {
  return [
    {
      id: "01JB7Z00000000000000000001",
      slug: "night-market",
      kind: "artwork",
      visibility: "public",
      title: "Night Market",
      tags: ["ink"],
      assets: [{ role: "original", blob: "sha256:" + "a".repeat(64) }],
      placements: [{ surface: "holodeck-art-room", config: {} }],
    } as Item,
    {
      id: "01JB7Z00000000000000000002",
      slug: "fog-bridge",
      kind: "photo",
      visibility: "draft",
      title: "Fog Bridge",
    } as Item,
  ];
}

export const surfaces: SurfaceInfo[] = [
  { id: "holodeck-art-room", label: "Art Room", baseUrl: null, registered: true, viewVersion: 1, placements: 1 },
  { id: "nickel-desktop", label: "Desktop", baseUrl: null, registered: true, viewVersion: 1, placements: 0 },
];

/**
 * A fake local API: the routes the UI uses, an in-memory working manifest
 * with revisions (If-Match is enforced), and an event stream tests push to.
 */
export function fakeApi(overrides: Record<string, Handler> = {}) {
  const state = { items: makeItems(), revision: "rev1", n: 1 };
  const calls: Call[] = [];
  let push: ((e: CanonEvent) => void) | null = null;
  const pending: CanonEvent[] = [];
  const enc = new TextEncoder();

  const routes: Record<string, Handler> = {
    "GET /api/items": () => json({ revision: state.revision, items: state.items }, 200, { ETag: `"${state.revision}"` }),
    "GET /api/items/:id": (_c, u) => {
      const id = decodeURIComponent(u.pathname.split("/").pop()!);
      const it = state.items.find((i) => i.id === id || i.slug === id);
      return it ? json(it, 200, { ETag: `"${state.revision}"` }) : json({ title: "Not found" }, 404);
    },
    "PATCH /api/items/:id": (c, u) => {
      if (c.headers.get("If-Match") !== `"${state.revision}"`) return json({ title: "Precondition failed" }, 412);
      const id = decodeURIComponent(u.pathname.split("/").pop()!);
      const i = state.items.findIndex((x) => x.id === id);
      const next = { ...state.items[i], ...(c.body as object) } as Item;
      for (const [k, v] of Object.entries(c.body as object)) if (v === null) delete (next as Record<string, unknown>)[k];
      state.items[i] = next;
      state.revision = `rev${++state.n}`;
      return json(next, 200, { ETag: `"${state.revision}"` });
    },
    "GET /api/taxonomy": () => json(taxonomy),
    "GET /api/surfaces": () => json(surfaces),
    "GET /api/sync/status": () => json({ state: "in-sync", baseGeneration: 1, remoteGeneration: 1, localEdits: false, revision: state.revision }),
    "GET /api/local/thumbnails/:sha": () => new Response(new Blob(["x"], { type: "image/webp" })),
    "GET /api/local/derived/:k/:f": () => new Response(new Blob(["x"], { type: "image/webp" })),
    "GET /api/jobs": () => json([]),
    "GET /api/jobs/run": () => json({ state: "done", report: null, error: null }),
    "GET /api/doctor": () => json({ checks: [{ name: "ffmpeg", status: "ok", detail: "6.1" }], tools: [] }),
    "GET /api/events": () =>
      new Response(
        new ReadableStream({
          start(ctl) {
            push = (e) => ctl.enqueue(enc.encode(`event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`));
            ctl.enqueue(enc.encode(": canon events\n\n"));
            pending.splice(0).forEach((e) => push!(e));
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
    ...overrides,
  };

  const match = (method: string, path: string): Handler | undefined => {
    if (routes[`${method} ${path}`]) return routes[`${method} ${path}`];
    const parts = path.split("/");
    for (const [k, h] of Object.entries(routes)) {
      const [m, p] = k.split(" ");
      const rp = (p ?? "").split("/");
      if (m === method && rp.length === parts.length && rp.every((x, i) => x.startsWith(":") || x === parts[i])) return h;
    }
    return undefined;
  };

  const fetchImpl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    // jsdom's FormData isn't the one Node's Request takes, so multipart
    // bodies are recorded as given.
    const form = init?.body instanceof FormData ? init.body : undefined;
    const req =
      input instanceof Request
        ? input
        : new Request(new URL(String(input), "http://127.0.0.1:4747"), form ? { ...init, body: undefined } : init);
    const url = new URL(req.url, "http://127.0.0.1:4747");
    let body: unknown = form;
    const type = req.headers.get("Content-Type") ?? "";
    if (!form && req.method !== "GET" && req.method !== "HEAD" && type.includes("json")) body = await req.clone().json();
    const call = { method: req.method, path: url.pathname, headers: req.headers, body };
    calls.push(call);
    if (req.headers.get("Authorization") !== "Bearer t0k") return json({ title: "Unauthorized" }, 401);
    const h = match(req.method, url.pathname);
    return h ? h(call, url) : json({ title: "No route in the fake", detail: `${req.method} ${url.pathname}` }, 404);
  }) as typeof fetch;

  return {
    fetchImpl,
    calls,
    state,
    routes,
    /** Sends an event down every open stream (or the next one). */
    emit(e: CanonEvent) {
      if (push) push(e);
      else pending.push(e);
    },
    json,
  };
}

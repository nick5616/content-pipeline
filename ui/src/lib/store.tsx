import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createApi, type Api } from "../api";
import { streamEvents } from "./events";
import type { CanonEvent, Item, Problem, SurfaceInfo, SyncStatus, Taxonomy, ValidationProblem } from "./types";

type Listener = (e: CanonEvent) => void;

/** What every screen shares: the API, the working manifest's items, and live events. */
export interface Store {
  api: Api;
  token: string;
  /** fetch with nothing added: for multipart uploads and raw requests. */
  fetch: typeof fetch;
  items: Item[];
  revision: string;
  taxonomy: Taxonomy | null;
  surfaces: SurfaceInfo[];
  sync: SyncStatus | null;
  /** Whether the first load is done. */
  ready: boolean;
  error: string | null;
  reload: () => Promise<void>;
  /** Subscribes to events; returns the unsubscribe. */
  on: (fn: Listener) => () => void;
  /** Merge-patches an item against the current revision. */
  patchItem: (id: string, patch: Record<string, unknown>) => Promise<PatchResult>;
}

export type PatchResult =
  | { ok: true; item: Item }
  | { ok: false; stale: true }
  | { ok: false; stale?: false; message: string; problems: ValidationProblem[] };

const Ctx = createContext<Store | null>(null);

/** Revisions travel as entity tags ("abc..."); compare and send them bare. */
const bare = (rev: string) => rev.replace(/"/g, "");

export function useStore(): Store {
  const s = useContext(Ctx);
  if (!s) throw new Error("useStore outside StoreProvider");
  return s;
}

/** Re-runs fn on events matching types (throttled), and once on mount. */
export function useOnEvents(types: CanonEvent["type"][], fn: (e?: CanonEvent) => void, deps: unknown[] = []) {
  const { on } = useStore();
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    let last: CanonEvent | undefined;
    const off = on((e) => {
      if (!types.includes(e.type)) return;
      last = e;
      if (!timer)
        timer = setTimeout(() => {
          timer = null;
          ref.current(last);
        }, 150);
    });
    return () => {
      off();
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [on, types.join(), ...deps]);
}

export function StoreProvider({
  token,
  fetchImpl = globalThis.fetch,
  children,
}: {
  token: string;
  fetchImpl?: typeof fetch;
  children: ReactNode;
}) {
  const api = useMemo(() => createApi(token, fetchImpl), [token, fetchImpl]);
  const [items, setItems] = useState<Item[]>([]);
  const [revision, setRevision] = useState("");
  const [taxonomy, setTaxonomy] = useState<Taxonomy | null>(null);
  const [surfaces, setSurfaces] = useState<SurfaceInfo[]>([]);
  const [sync, setSync] = useState<SyncStatus | null>(null);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const listeners = useRef(new Set<Listener>());
  const revisionRef = useRef("");

  const reload = useCallback(async () => {
    const [it, tx, sf] = await Promise.all([api.GET("/api/items"), api.GET("/api/taxonomy"), api.GET("/api/surfaces")]);
    if (it.error || !it.data) {
      setError(`Couldn't load items: ${it.response.status} ${(it.error as Problem | undefined)?.title ?? ""}`);
      return;
    }
    setError(null);
    setItems(it.data.items);
    setRevision(bare(it.data.revision));
    revisionRef.current = bare(it.data.revision);
    if (tx.data) setTaxonomy(tx.data);
    if (sf.data) setSurfaces(sf.data);
    setReady(true);
  }, [api]);

  const loadSync = useCallback(async () => {
    const { data } = await api.GET("/api/sync/status");
    setSync(data ?? null);
  }, [api]);

  useEffect(() => {
    reload();
    loadSync();
    const ctl = new AbortController();
    streamEvents(
      token,
      (e) => {
        if (e.type === "sync") setSync(e.data as SyncStatus);
        listeners.current.forEach((fn) => fn(e));
      },
      ctl.signal,
      () => {},
      fetchImpl,
    );
    return () => ctl.abort();
  }, [token, fetchImpl, reload, loadSync]);

  // Anything that changes the manifest elsewhere (the CLI, an apply, an
  // ingest) shows up as a sync or ingest event: reload then.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const fn: Listener = (e) => {
      if (e.type === "sync" || (e.type === "ingest" && JSON.stringify(e.data).includes('"stage":"done"'))) {
        if (timer) clearTimeout(timer);
        timer = setTimeout(() => {
          const s = e.type === "sync" ? (e.data as SyncStatus) : null;
          if (!s || bare(s.revision) !== revisionRef.current) reload();
        }, 200);
      }
    };
    listeners.current.add(fn);
    return () => {
      listeners.current.delete(fn);
    };
  }, [reload]);

  const on = useCallback((fn: Listener) => {
    listeners.current.add(fn);
    return () => {
      listeners.current.delete(fn);
    };
  }, []);

  const patchItem = useCallback(
    async (id: string, patch: Record<string, unknown>): Promise<PatchResult> => {
      const res = await fetchImpl(`/api/items/${encodeURIComponent(id)}`, {
        method: "PATCH",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/merge-patch+json",
          "If-Match": `"${revisionRef.current}"`,
        },
        body: JSON.stringify(patch),
      });
      if (res.status === 412) {
        await reload();
        return { ok: false, stale: true };
      }
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        return { ok: false, message: body.detail ?? body.title ?? String(res.status), problems: body.problems ?? [] };
      }
      const etag = res.headers.get("ETag") && bare(res.headers.get("ETag")!);
      if (etag) {
        revisionRef.current = etag;
        setRevision(etag);
      }
      const item = body as Item;
      setItems((all) => all.map((x) => (x.id === item.id ? item : x)));
      return { ok: true, item };
    },
    [fetchImpl, token, reload],
  );

  const store: Store = {
    api,
    token,
    fetch: fetchImpl,
    items,
    revision,
    taxonomy,
    surfaces,
    sync,
    ready,
    error,
    reload,
    on,
    patchItem,
  };
  return <Ctx.Provider value={store}>{children}</Ctx.Provider>;
}

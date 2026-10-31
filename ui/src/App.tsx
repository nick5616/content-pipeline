import { useEffect, useState } from "react";
import { takeToken } from "./token";
import { StoreProvider, useStore } from "./lib/store";
import { go, href, useRoute, type Route } from "./lib/route";
import { Library } from "./screens/Library";
import { ItemEditor } from "./screens/ItemEditor";
import { PlanReview } from "./screens/PlanReview";
import { Ingest } from "./screens/Ingest";
import { Jobs } from "./screens/Jobs";
import { Surfaces } from "./screens/Surfaces";
import { Doctor } from "./screens/Doctor";

export function App({ token: given, fetchImpl }: { token?: string; fetchImpl?: typeof fetch }) {
  const [token] = useState(() => given ?? takeToken());
  if (!token) {
    return (
      <main className="empty">
        <h1>Canon</h1>
        <p className="error">
          No token. Open the URL that <code>canon serve</code> printed.
        </p>
      </main>
    );
  }
  return (
    <StoreProvider token={token} fetchImpl={fetchImpl}>
      <Shell />
    </StoreProvider>
  );
}

const NAV: { label: string; route: Route; key?: string }[] = [
  { label: "Library", route: { screen: "library" } },
  { label: "Plan", route: { screen: "plan" }, key: "p" },
  { label: "Ingest", route: { screen: "ingest" } },
  { label: "Jobs", route: { screen: "jobs" } },
  { label: "Surfaces", route: { screen: "surfaces" } },
  { label: "Doctor", route: { screen: "doctor" } },
];

function Shell() {
  const route = useRoute();
  const { sync, error, ready } = useStore();

  // Global keys: p plans, Esc goes back to the library. Screens add their
  // own (/, e, j, k in the library). Typing in a field never triggers them.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (isTyping(e)) {
        if (e.key === "Escape") (document.activeElement as HTMLElement)?.blur();
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === "p") go({ screen: "plan" });
      else if (e.key === "Escape" && route.screen !== "library") go({ screen: "library" });
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [route.screen]);

  return (
    <div className="shell">
      <header className="bar">
        <a className="brand" href="#/">
          Canon
        </a>
        <nav>
          {NAV.map((n) => (
            <a
              key={n.label}
              href={href(n.route)}
              className={n.route.screen === route.screen || (n.route.screen === "library" && route.screen === "item") ? "on" : ""}
            >
              {n.label}
              {n.key && <kbd>{n.key}</kbd>}
            </a>
          ))}
        </nav>
        <SyncPill />
      </header>
      {error && <p className="banner error">{error}</p>}
      {sync?.state === "diverged" && (
        <p className="banner warn">
          The bucket's manifest and your working copy both changed. Run <code>canon pull --discard-local</code> or resolve by
          hand; Canon never merges.
        </p>
      )}
      <main className="screen">{!ready && !error ? <p className="muted">Loading…</p> : <Screen route={route} />}</main>
    </div>
  );
}

function Screen({ route }: { route: Route }) {
  switch (route.screen) {
    case "item":
      return <ItemEditor id={route.id} />;
    case "plan":
      return <PlanReview />;
    case "ingest":
      return <Ingest />;
    case "jobs":
      return <Jobs />;
    case "surfaces":
      return <Surfaces id={route.id} />;
    case "doctor":
      return <Doctor />;
    default:
      return <Library />;
  }
}

const SYNC_LABEL: Record<string, string> = {
  "in-sync": "In sync",
  edited: "Local edits",
  behind: "Bucket moved",
  diverged: "Diverged",
  offline: "Offline",
};

function SyncPill() {
  const { sync } = useStore();
  // No status: no buckets configured, offline, or not checked yet.
  if (!sync) return <span className="pill muted" title="No buckets configured, or the bucket can't be reached">Sync unknown</span>;
  const state = sync.state;
  return (
    <a className={`pill sync-${state}`} href="#/plan" title={`Working revision ${sync.revision}`}>
      {SYNC_LABEL[state] ?? state}
    </a>
  );
}

export function isTyping(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
}

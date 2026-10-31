import { useEffect, useState } from "react";
import { Sketch } from "../surfaces/Sketches";
import { useStore } from "../lib/store";
import { href } from "../lib/route";
import type { SurfaceChanges } from "../lib/types";

/** The declared surfaces, and what each will show after the next apply. */
export function Surfaces({ id }: { id?: string }) {
  const { surfaces } = useStore();
  const current = id ?? surfaces[0]?.id;
  return (
    <section className="surfaces">
      <nav className="tabs">
        {surfaces.map((s) => (
          <a key={s.id} href={href({ screen: "surfaces", id: s.id })} className={s.id === current ? "on" : ""}>
            {s.label} <span className="count">{s.placements}</span>
          </a>
        ))}
      </nav>
      {surfaces.length === 0 && (
        <p className="muted">
          No surfaces declared yet: <code>canon surface set &lt;id&gt; label=...</code>
        </p>
      )}
      {current && <Preview key={current} surface={current} />}
    </section>
  );
}

function Preview({ surface }: { surface: string }) {
  const { api, revision } = useStore();
  const [view, setView] = useState<Record<string, unknown> | null>(null);
  const [changes, setChanges] = useState<SurfaceChanges | null>(null);
  const [raw, setRaw] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api.GET("/api/surfaces/{id}/preview", { params: { path: { id: surface } } }).then(({ data, error: err }) => {
      if (!live) return;
      if (data) setView(data as Record<string, unknown>);
      else setError((err as { detail?: string })?.detail ?? "Couldn't resolve the view");
    });
    api.GET("/api/surfaces/{id}/changes", { params: { path: { id: surface } } }).then(({ data }) => live && setChanges(data ?? null));
    return () => {
      live = false;
    };
  }, [api, surface, revision]);

  if (error) return <p className="error">{error}</p>;
  if (!view) return <p className="muted">Resolving…</p>;
  return (
    <div className="preview">
      <Changes changes={changes} />
      <div className="toolbar">
        <span className="muted small">What {surface} gets from the next apply. Drafts never appear.</span>
        <button className="link" onClick={() => setRaw(!raw)}>
          {raw ? "Show the sketch" : "Show the view JSON"}
        </button>
      </div>
      {raw ? <pre className="json">{JSON.stringify(view, null, 2)}</pre> : <Sketch surface={surface} view={view as never} />}
    </div>
  );
}

function Changes({ changes }: { changes: SurfaceChanges | null }) {
  if (!changes) return null;
  const n = changes.added.length + changes.removed.length + changes.changed.length;
  if (!changes.live) return <p className="banner info">Not published yet: the next apply creates this view.</p>;
  if (n === 0 && !changes.header) return <p className="ok">Matches the live site's view.</p>;
  return (
    <div className="changes">
      <strong>Changes since the live view</strong>
      <ul>
        {changes.added.map((e) => (
          <li key={"a" + e.id + e.key} className="add">
            + {e.title}
          </li>
        ))}
        {changes.removed.map((e) => (
          <li key={"r" + e.id + e.key} className="remove">
            − {e.title}
          </li>
        ))}
        {changes.changed.map((e) => (
          <li key={"c" + e.id + e.key} className="change">
            ~ {e.title} <span className="muted">({e.fields?.join(", ")})</span>
          </li>
        ))}
        {changes.header && <li className="change">~ the view's header (profile, taxonomy, tracks or filters)</li>}
      </ul>
    </div>
  );
}

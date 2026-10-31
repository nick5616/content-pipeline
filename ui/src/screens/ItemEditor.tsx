import { useCallback, useEffect, useRef, useState } from "react";
import { defaultConfig, PlacementForm } from "../forms/PlacementForm";
import { useStore } from "../lib/store";
import { thumbnailUrl, useAuthedImage } from "../lib/images";
import { href } from "../lib/route";
import { VOCABS, type Credit, type Item, type Link, type Placement, type Term, type ValidationProblem } from "../lib/types";

type Save = { state: "idle" } | { state: "saving" } | { state: "saved" } | { state: "error"; message: string; problems: ValidationProblem[] };

const LINK_RELS = ["live", "source", "backend-source", "docs", "video", "listen"];

/**
 * Edits one item. Every field saves on its own a moment after you stop
 * typing, as a merge patch on the working manifest only; nothing here
 * touches the cloud. If the manifest moved underneath (the CLI, another
 * tab), the item reloads and your change is applied again on top.
 */
export function ItemEditor({ id }: { id: string }) {
  const { api, items, taxonomy, surfaces, patchItem } = useStore();
  const [item, setItem] = useState<Item | null>(null);
  const [missing, setMissing] = useState(false);
  const [save, setSave] = useState<Save>({ state: "idle" });
  const [note, setNote] = useState("");
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  const load = useCallback(async () => {
    const { data, response } = await api.GET("/api/items/{id}", { params: { path: { id } } });
    if (response.status === 404) setMissing(true);
    else if (data) setItem(data);
  }, [api, id]);

  useEffect(() => {
    setItem(null);
    setMissing(false);
    load();
  }, [load]);

  const send = useCallback(
    async (patch: Record<string, unknown>, retried = false) => {
      if (!item) return;
      setSave({ state: "saving" });
      const r = await patchItem(item.id, patch);
      if (r.ok) {
        setItem(r.item);
        setSave({ state: "saved" });
      } else if (r.stale) {
        if (retried) {
          setSave({ state: "error", message: "The manifest keeps changing underneath; reload and try again.", problems: [] });
          return;
        }
        setNote("The manifest changed elsewhere (the CLI or another tab); your change was applied on top of it.");
        await send(patch, true);
      } else {
        setSave({ state: "error", message: r.message, problems: r.problems });
      }
    },
    [item, patchItem],
  );

  /** Updates the item locally now and saves the field after a pause. */
  const set = useCallback(
    (field: keyof Item, value: unknown, delay = 500) => {
      setItem((it) => (it ? ({ ...it, [field]: value === null ? undefined : value } as Item) : it));
      const t = timers.current.get(field);
      if (t) clearTimeout(t);
      timers.current.set(
        field,
        setTimeout(() => {
          timers.current.delete(field);
          send({ [field]: value });
        }, delay),
      );
    },
    [send],
  );

  // Save anything pending before leaving.
  useEffect(() => {
    const ts = timers.current;
    return () => ts.forEach((t) => clearTimeout(t));
  }, []);

  if (missing) return <p className="error">No item {id}.</p>;
  if (!item) return <p className="muted">Loading…</p>;

  const members = (item.members ?? []).map((m) => items.find((i) => i.id === m));
  return (
    <article className="editor">
      <header className="editor-head">
        <a href="#/" className="muted">
          ← Library <kbd>Esc</kbd>
        </a>
        <SaveState save={save} />
      </header>
      {note && (
        <p className="banner warn" onClick={() => setNote("")}>
          {note}
        </p>
      )}
      {save.state === "error" && save.problems.length > 0 && (
        <ul className="problems">
          {save.problems.map((p, i) => (
            <li key={i} className={p.severity}>
              <code>{p.path}</code> {p.message}
            </li>
          ))}
        </ul>
      )}

      <div className="editor-grid">
        <div className="col">
          <label className="field">
            <span className="field-label">Title</span>
            <input className="big" value={item.title} onChange={(e) => set("title", e.target.value)} />
          </label>
          <p className="muted small">
            <code>{item.slug}</code> · {item.kind}
            {item.aliases?.length ? <> · also {item.aliases.join(", ")}</> : null}
          </p>
          <div className="row">
            <label className="field inline">
              <input
                type="checkbox"
                checked={item.visibility === "public"}
                onChange={(e) => set("visibility", e.target.checked ? "public" : "draft", 0)}
              />
              <span>Public</span>
            </label>
            <span className="muted small">
              {item.visibility === "public" ? "Published on the next apply." : "A draft: never leaves the private bucket."}
            </span>
          </div>
          <label className="field">
            <span className="field-label">Summary</span>
            <textarea rows={3} value={item.summary ?? ""} onChange={(e) => set("summary", e.target.value || null)} />
          </label>
          <label className="field">
            <span className="field-label">Why</span>
            <textarea rows={3} value={item.why ?? ""} onChange={(e) => set("why", e.target.value || null)} />
          </label>
          <div className="row">
            <label className="field">
              <span className="field-label">Date</span>
              <input
                placeholder="2024, 2024-12 or 2024-12-03"
                value={item.date?.value ?? ""}
                onChange={(e) => set("date", e.target.value ? { ...item.date, value: e.target.value } : null, 900)}
              />
            </label>
            <label className="field grow">
              <span className="field-label">Shown as</span>
              <input
                placeholder="Circa 2022 (optional)"
                value={item.date?.label ?? ""}
                disabled={!item.date}
                onChange={(e) => set("date", { value: item.date!.value, ...(e.target.value ? { label: e.target.value } : {}) })}
              />
            </label>
          </div>
          <div className="row">
            <label className="field">
              <span className="field-label">Status</span>
              <input value={item.status ?? ""} onChange={(e) => set("status", e.target.value || null)} />
            </label>
            <label className="field grow">
              <span className="field-label">Category</span>
              <select value={item.category ?? ""} onChange={(e) => set("category", e.target.value || null, 0)}>
                <option value="">(none)</option>
                {taxonomy?.categories.map((c) => (
                  <option key={c.slug} value={c.slug}>
                    {c.icon} {c.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          {VOCABS.filter((v) => v !== "categories").map((v) => (
            <TermPicker
              key={v}
              label={v}
              terms={taxonomy?.[v] ?? []}
              value={(item[v as "tech" | "tags" | "mediums"] ?? []) as string[]}
              onChange={(next) => set(v as keyof Item, next.length ? next : null, 0)}
            />
          ))}
          <Links links={item.links ?? []} onChange={(l) => set("links", l.length ? l : null)} />
          <Credits credits={item.credits ?? []} onChange={(c) => set("credits", c.length ? c : null)} />
          <label className="field">
            <span className="field-label">Private notes</span>
            <textarea
              rows={3}
              className="private"
              value={item.private?.notes ?? ""}
              onChange={(e) => set("private", e.target.value ? { notes: e.target.value } : null)}
            />
            <span className="muted small">Never published: the public manifest leaves private out.</span>
          </label>
        </div>

        <div className="col">
          <Assets item={item} onChange={(a) => set("assets", a)} />
          {item.kind === "collection" && (
            <section>
              <h3>Members ({members.length})</h3>
              <ol className="members">
                {members.map((m, i) => (
                  <li key={item.members![i]}>
                    {m ? <a href={href({ screen: "item", id: m.id })}>{m.title}</a> : <code>{item.members![i]}</code>}
                    {m?.visibility === "draft" && <span className="tag draft">draft</span>}
                  </li>
                ))}
              </ol>
            </section>
          )}
          <Placements
            placements={item.placements ?? []}
            surfaces={surfaces.map((s) => s.id)}
            onChange={(p) => set("placements", p.length ? p : null, 700)}
          />
          <CopyOverrides
            copy={item.copy ?? {}}
            surfaces={surfaces.map((s) => s.id)}
            onChange={(c) => set("copy", Object.keys(c).length ? c : null)}
          />
          {item.facts && Object.keys(item.facts).length > 0 && (
            <section>
              <h3>Facts</h3>
              <p className="muted small">Computed by ingest and jobs; read-only.</p>
              <dl className="facts">
                {Object.entries(item.facts).map(([k, v]) => (
                  <div key={k}>
                    <dt>{k}</dt>
                    <dd>{Array.isArray(v) ? <Palette v={v as string[]} /> : String(v)}</dd>
                  </div>
                ))}
              </dl>
            </section>
          )}
        </div>
      </div>
    </article>
  );
}

function SaveState({ save }: { save: Save }) {
  switch (save.state) {
    case "saving":
      return <span className="muted">Saving…</span>;
    case "saved":
      return <span className="ok">Saved to the working manifest</span>;
    case "error":
      return <span className="error">Not saved: {save.message}</span>;
    default:
      return <span className="muted">Edits save as you type</span>;
  }
}

function Palette({ v }: { v: string[] }) {
  if (!v.every((c) => /^#[0-9a-f]{6}$/.test(c))) return <>{v.join(", ")}</>;
  return (
    <span className="palette">
      {v.map((c) => (
        <span key={c} style={{ background: c }} title={c} />
      ))}
    </span>
  );
}

function TermPicker({ label, terms, value, onChange }: { label: string; terms: Term[]; value: string[]; onChange: (v: string[]) => void }) {
  const byslug = new Map(terms.map((t) => [t.slug, t]));
  return (
    <div className="field">
      <span className="field-label">{label}</span>
      <div className="chips">
        {value.map((s) => (
          <button key={s} type="button" className="chip" onClick={() => onChange(value.filter((x) => x !== s))} title="Remove">
            {byslug.get(s)?.label ?? s} ×
          </button>
        ))}
        <select
          value=""
          aria-label={`Add ${label}`}
          onChange={(e) => e.target.value && onChange([...value, e.target.value])}
        >
          <option value="">+ add</option>
          {terms
            .filter((t) => !value.includes(t.slug))
            .map((t) => (
              <option key={t.slug} value={t.slug}>
                {t.label}
              </option>
            ))}
        </select>
      </div>
    </div>
  );
}

function Links({ links, onChange }: { links: Link[]; onChange: (l: Link[]) => void }) {
  const set = (i: number, l: Partial<Link>) => onChange(links.map((x, j) => (j === i ? ({ ...x, ...l } as Link) : x)));
  return (
    <section className="field">
      <span className="field-label">Links</span>
      {links.map((l, i) => (
        <div className="row" key={i}>
          <select value={l.rel} onChange={(e) => set(i, { rel: e.target.value as Link["rel"] })} aria-label="Link type">
            {LINK_RELS.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
          <input className="grow" value={l.url} onChange={(e) => set(i, { url: e.target.value })} aria-label="URL" />
          <button type="button" className="link" onClick={() => onChange(links.filter((_, j) => j !== i))}>
            Remove
          </button>
        </div>
      ))}
      <button type="button" className="link" onClick={() => onChange([...links, { rel: "live", url: "https://" } as Link])}>
        + link
      </button>
    </section>
  );
}

function Credits({ credits, onChange }: { credits: Credit[]; onChange: (c: Credit[]) => void }) {
  const set = (i: number, c: Partial<Credit>) => onChange(credits.map((x, j) => (j === i ? ({ ...x, ...c } as Credit) : x)));
  return (
    <section className="field">
      <span className="field-label">Credits</span>
      {credits.map((c, i) => (
        <div className="row" key={i}>
          <input className="grow" value={c.name} onChange={(e) => set(i, { name: e.target.value })} aria-label="Name" />
          <input value={c.role ?? ""} placeholder="role" onChange={(e) => set(i, { role: e.target.value || undefined })} aria-label="Role" />
          <button type="button" className="link" onClick={() => onChange(credits.filter((_, j) => j !== i))}>
            Remove
          </button>
        </div>
      ))}
      <button type="button" className="link" onClick={() => onChange([...credits, { name: "", role: "artist" }])}>
        + credit
      </button>
    </section>
  );
}

function Assets({ item, onChange }: { item: Item; onChange: (a: NonNullable<Item["assets"]>) => void }) {
  const assets = item.assets ?? [];
  if (!assets.length) return null;
  return (
    <section>
      <h3>Assets</h3>
      {assets.map((a, i) => (
        <AssetRow
          key={a.blob + i}
          asset={a}
          onAlt={(alt) => onChange(assets.map((x, j) => (j === i ? { ...x, alt: alt || undefined } : x)))}
        />
      ))}
    </section>
  );
}

function AssetRow({ asset, onAlt }: { asset: NonNullable<Item["assets"]>[number]; onAlt: (alt: string) => void }) {
  const { token } = useStore();
  const sha = asset.blob.replace(/^sha256:/, "");
  const image = ["original", "thumbnail", "cover", "screenshot"].includes(asset.role);
  const src = useAuthedImage(image ? thumbnailUrl(sha, 800) : null, token);
  return (
    <div className="asset">
      {image && <div className="asset-img">{src ? <img src={src} alt={asset.alt ?? ""} /> : <span className="muted">no thumbnail yet</span>}</div>}
      <div className="grow">
        <p className="small">
          <span className="tag">{asset.role}</span> <code>{sha.slice(0, 12)}</code>
        </p>
        {image && (
          <label className="field">
            <span className="field-label">Alt text</span>
            <input value={asset.alt ?? ""} onChange={(e) => onAlt(e.target.value)} />
          </label>
        )}
      </div>
    </div>
  );
}

function Placements({ placements, surfaces, onChange }: { placements: Placement[]; surfaces: string[]; onChange: (p: Placement[]) => void }) {
  const [adding, setAdding] = useState("");
  const set = (i: number, p: Partial<Placement>) => onChange(placements.map((x, j) => (j === i ? ({ ...x, ...p } as Placement) : x)));
  return (
    <section>
      <h3>Placements</h3>
      {placements.length === 0 && <p className="muted small">Not on any surface. A placement is what puts an item on a site.</p>}
      {placements.map((p, i) => (
        <details key={i} className="placement" open={placements.length < 3}>
          <summary>
            <strong>{p.surface}</strong>
            {p.key && <> · {p.key}</>}
            {p.order != null && <span className="muted"> · order {p.order}</span>}
            {p.hidden && <span className="tag draft">hidden</span>}
          </summary>
          <div className="row">
            <label className="field">
              <span className="field-label">order</span>
              <input
                type="number"
                value={p.order ?? ""}
                onChange={(e) => set(i, { order: e.target.value === "" ? undefined : Number(e.target.value) })}
              />
            </label>
            <label className="field">
              <span className="field-label">key</span>
              <input value={p.key ?? ""} onChange={(e) => set(i, { key: e.target.value || undefined })} />
            </label>
            <label className="field inline">
              <input type="checkbox" checked={!!p.featured} onChange={(e) => set(i, { featured: e.target.checked || undefined })} />
              <span>featured</span>
            </label>
            <label className="field inline">
              <input type="checkbox" checked={!!p.hidden} onChange={(e) => set(i, { hidden: e.target.checked || undefined })} />
              <span>hidden</span>
            </label>
          </div>
          <PlacementForm surface={p.surface} value={(p.config ?? {}) as Record<string, unknown>} onChange={(config) => set(i, { config: config as Placement["config"] })} />
          <label className="field">
            <span className="field-label">Title on this placement only</span>
            <input
              value={p.copy?.title ?? ""}
              onChange={(e) => set(i, { copy: e.target.value ? { ...p.copy, title: e.target.value } : undefined })}
            />
          </label>
          <button type="button" className="link danger" onClick={() => onChange(placements.filter((_, j) => j !== i))}>
            Remove from {p.surface}
          </button>
        </details>
      ))}
      <div className="row">
        <select value={adding} onChange={(e) => setAdding(e.target.value)} aria-label="Surface to add">
          <option value="">Place on…</option>
          {surfaces.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <button
          type="button"
          disabled={!adding}
          onClick={() => {
            onChange([...placements, { surface: adding, config: defaultConfig(adding) } as Placement]);
            setAdding("");
          }}
        >
          Add placement
        </button>
      </div>
    </section>
  );
}

function CopyOverrides({
  copy,
  surfaces,
  onChange,
}: {
  copy: NonNullable<Item["copy"]>;
  surfaces: string[];
  onChange: (c: NonNullable<Item["copy"]>) => void;
}) {
  const set = (s: string, f: "title" | "summary" | "why", v: string) => {
    const next = { ...copy, [s]: { ...copy[s], [f]: v || undefined } };
    if (Object.values(next[s] ?? {}).every((x) => x === undefined)) delete next[s];
    onChange(next);
  };
  const shown = surfaces.filter((s) => copy[s]);
  const [extra, setExtra] = useState("");
  return (
    <section>
      <h3>Wording per surface</h3>
      <p className="muted small">Overrides the item's title, summary or why on one surface only.</p>
      {[...shown, ...(extra && !shown.includes(extra) ? [extra] : [])].map((s) => (
        <fieldset key={s} className="nested">
          <legend>{s}</legend>
          {(["title", "summary", "why"] as const).map((f) => (
            <label key={f} className="field">
              <span className="field-label">{f}</span>
              {f === "title" ? (
                <input value={copy[s]?.[f] ?? ""} onChange={(e) => set(s, f, e.target.value)} />
              ) : (
                <textarea rows={2} value={copy[s]?.[f] ?? ""} onChange={(e) => set(s, f, e.target.value)} />
              )}
            </label>
          ))}
        </fieldset>
      ))}
      <select value="" onChange={(e) => setExtra(e.target.value)} aria-label="Override wording on">
        <option value="">Override wording on…</option>
        {surfaces
          .filter((s) => !shown.includes(s))
          .map((s) => (
            <option key={s}>{s}</option>
          ))}
      </select>
    </section>
  );
}

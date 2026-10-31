import { useEffect, useMemo, useRef, useState } from "react";
import { isTyping } from "../App";
import { go, href } from "../lib/route";
import { useStore } from "../lib/store";
import { thumbnailUrl, useAuthedImage } from "../lib/images";
import { imageBlobs, type Item } from "../lib/types";

const KINDS = ["project", "artwork", "photo", "track", "video", "writing", "collection", "experience"];

/** Every item, filtered by kind, surface, visibility, tag and text. */
export function Library() {
  const { items, surfaces, taxonomy } = useStore();
  const [q, setQ] = useState("");
  const [kind, setKind] = useState("");
  const [surface, setSurface] = useState("");
  const [vis, setVis] = useState("");
  const [tag, setTag] = useState("");
  const [sel, setSel] = useState(0);
  const search = useRef<HTMLInputElement>(null);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return items
      .filter((it) => !kind || it.kind === kind)
      .filter((it) => !vis || it.visibility === vis)
      .filter((it) => !surface || (it.placements ?? []).some((p) => p.surface === surface && !p.hidden))
      .filter((it) => !tag || (it.tags ?? []).includes(tag))
      .filter(
        (it) =>
          !needle ||
          it.title.toLowerCase().includes(needle) ||
          it.slug.includes(needle) ||
          (it.aliases ?? []).some((a) => a.includes(needle)),
      )
      .sort((a, b) => a.title.localeCompare(b.title));
  }, [items, q, kind, surface, vis, tag]);

  useEffect(() => setSel(0), [q, kind, surface, vis, tag]);

  // / searches; j and k move; e or Enter edits.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (isTyping(e) || e.metaKey || e.ctrlKey || e.altKey) {
        if (e.key === "Enter" && e.target === search.current && shown[0]) go({ screen: "item", id: shown[sel]?.id ?? shown[0].id });
        return;
      }
      if (e.key === "/") {
        e.preventDefault();
        search.current?.focus();
      } else if (e.key === "j") setSel((s) => Math.min(s + 1, shown.length - 1));
      else if (e.key === "k") setSel((s) => Math.max(s - 1, 0));
      else if ((e.key === "e" || e.key === "Enter") && shown[sel]) go({ screen: "item", id: shown[sel].id });
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [shown, sel]);

  useEffect(() => {
    document.querySelector(`[data-index="${sel}"]`)?.scrollIntoView?.({ block: "nearest" });
  }, [sel]);

  const drafts = items.filter((i) => i.visibility === "draft").length;
  return (
    <section className="library">
      <div className="toolbar">
        <input
          ref={search}
          type="search"
          placeholder="Search titles, slugs, aliases  /"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label="Search"
        />
        <select value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Kind">
          <option value="">Every kind</option>
          {KINDS.map((k) => (
            <option key={k}>{k}</option>
          ))}
        </select>
        <select value={surface} onChange={(e) => setSurface(e.target.value)} aria-label="Surface">
          <option value="">Every surface</option>
          {surfaces.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label}
            </option>
          ))}
        </select>
        <select value={vis} onChange={(e) => setVis(e.target.value)} aria-label="Visibility">
          <option value="">Public and drafts</option>
          <option value="public">Public</option>
          <option value="draft">Drafts</option>
        </select>
        <select value={tag} onChange={(e) => setTag(e.target.value)} aria-label="Tag">
          <option value="">Every tag</option>
          {(taxonomy?.tags ?? []).map((t) => (
            <option key={t.slug} value={t.slug}>
              {t.label}
            </option>
          ))}
        </select>
        <span className="muted count">
          {shown.length} of {items.length} · {drafts} drafts
        </span>
      </div>
      {shown.length === 0 ? (
        <p className="muted">Nothing matches.</p>
      ) : (
        <ul className="grid" role="listbox" aria-label="Items">
          {shown.map((it, i) => (
            <Card key={it.id} item={it} index={i} selected={i === sel} />
          ))}
        </ul>
      )}
    </section>
  );
}

function Card({ item, index, selected }: { item: Item; index: number; selected: boolean }) {
  const { token } = useStore();
  const sha = imageBlobs(item)[0];
  const src = useAuthedImage(sha ? thumbnailUrl(sha, 320) : null, token);
  const surfaces = [...new Set((item.placements ?? []).filter((p) => !p.hidden).map((p) => p.surface))];
  return (
    <li role="option" aria-selected={selected} data-index={index} className={`card${selected ? " selected" : ""}`}>
      <a href={href({ screen: "item", id: item.id })}>
        <div className="thumb">{src ? <img src={src} alt="" loading="lazy" /> : <span className="kind-glyph">{glyph(item.kind)}</span>}</div>
        <div className="meta">
          <span className="title">{item.title}</span>
          <span className="sub">
            <span className={`tag kind-${item.kind}`}>{item.kind}</span>
            {item.visibility === "draft" && <span className="tag draft">draft</span>}
            {surfaces.length > 0 && <span className="muted">{surfaces.length === 1 ? surfaces[0] : `${surfaces.length} surfaces`}</span>}
          </span>
        </div>
      </a>
    </li>
  );
}

function glyph(kind: string) {
  return { project: "◆", artwork: "▣", photo: "◩", track: "♪", video: "▶", writing: "¶", collection: "▤", experience: "◎" }[kind] ?? "·";
}

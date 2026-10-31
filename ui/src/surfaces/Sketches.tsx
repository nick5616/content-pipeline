import { localize, useAuthedImage } from "../lib/images";
import { useStore } from "../lib/store";

// A faithful sketch per surface (D63): each site's real structure, drawn from
// the view the next apply would publish. Images come from the local cache,
// since the public bucket doesn't have unpublished files yet.

type Img = { url: string; width: number; height: number; srcset: { url: string; width: number }[]; alt: string | null } | null;
type Entry = Record<string, unknown> & { id: string; slug: string; key: string | null; title: string; summary: string };
type View = Record<string, unknown> & { entries: Entry[] };

export function Sketch({ surface, view }: { surface: string; view: View }) {
  switch (surface) {
    case "holodeck-art-room":
      return <ArtRoom view={view} />;
    case "holodeck-dj-room":
      return <DjRoom view={view} />;
    case "nickel-desktop":
      return <Desktop view={view} />;
    case "art-portfolio":
      return <ArtPortfolio view={view} />;
    default:
      return <Cards view={view} />;
  }
}

/** The smallest published size at least w wide, served from the cache. */
function Picture({ image, w = 320 }: { image: Img; w?: number }) {
  const { token } = useStore();
  const pick = image ? (image.srcset.find((s) => s.width >= w) ?? image.srcset.at(-1) ?? image) : null;
  const src = useAuthedImage(pick ? localize(pick.url) : null, token);
  if (!image) return <div className="pic none" />;
  return (
    <div className="pic" style={{ aspectRatio: `${image.width} / ${image.height}` }}>
      {src && <img src={src} alt={image.alt ?? ""} />}
    </div>
  );
}

function ArtRoom({ view }: { view: View }) {
  return (
    <div className="sk-artroom">
      {view.entries.map((hall) => {
        const pieces = (hall.pieces as { id: string; title: string | null; description: string | null; date: { display: string } | null; image: Img }[]) ?? [];
        return (
          <section key={hall.id} className="hall">
            <header>
              <h3>{hall.title}</h3>
              <p className="muted">{hall.summary}</p>
            </header>
            <div className="wall">
              {pieces.map((p) => (
                <figure key={p.id}>
                  <Picture image={p.image} w={320} />
                  {p.title && (
                    <figcaption className="plaque">
                      <strong>{p.title}</strong>
                      {p.date && <span> · {p.date.display}</span>}
                      {p.description && <span className="muted"> {p.description}</span>}
                    </figcaption>
                  )}
                </figure>
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

function DjRoom({ view }: { view: View }) {
  const tracks = (view.tracks as { id: string; title: string; artist: string; bpm: number; key: string; camelot: string; duration: number }[]) ?? [];
  return (
    <div className="sk-dj">
      <p className="muted">Crate: {String(view.artist)}</p>
      <table className="table">
        <thead>
          <tr>
            <th>Track</th>
            <th>Artist</th>
            <th>BPM</th>
            <th>Key</th>
            <th>Length</th>
          </tr>
        </thead>
        <tbody>
          {tracks.map((t) => (
            <tr key={t.id}>
              <td>{t.title}</td>
              <td>{t.artist}</td>
              <td>{t.bpm}</td>
              <td>
                {t.key} <span className="muted">{t.camelot}</span>
              </td>
              <td>
                {Math.floor(t.duration / 60)}:{String(Math.round(t.duration % 60)).padStart(2, "0")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Desktop({ view }: { view: View }) {
  const docked = view.entries.filter((e) => e.pinned !== null).sort((a, b) => (a.pinned as number) - (b.pinned as number));
  const icon = (e: Entry) => (e.category as { icon?: string } | null)?.icon ?? "📄";
  return (
    <div className="sk-desktop">
      <div className="icons">
        {view.entries.map((e) => (
          <div key={e.id + (e.key ?? "")} className="icon" title={e.summary}>
            <span className="emoji">{icon(e)}</span>
            <span>{e.title}</span>
          </div>
        ))}
      </div>
      <div className="dock">
        {docked.map((e) => (
          <span key={e.id} className="emoji" title={e.title}>
            {icon(e)}
          </span>
        ))}
      </div>
    </div>
  );
}

function ArtPortfolio({ view }: { view: View }) {
  const section = (s: string) => view.entries.filter((e) => e.section === s);
  return (
    <div className="sk-art">
      {(["primary", "collections"] as const).map((s) => (
        <section key={s}>
          <h3>{s === "primary" ? "Featured" : "Collections"}</h3>
          <div className="cards">
            {section(s).map((e) => (
              <div key={e.id} className="sk-card">
                <Picture image={(e.image ?? e.cover) as Img} />
                <strong>{e.title}</strong>
                {e.memberCount != null && <span className="muted">{String(e.memberCount)} pieces</span>}
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

/** Software, UX and showroom: project cards with their preview kind. */
function Cards({ view }: { view: View }) {
  return (
    <div className="cards">
      {view.entries.map((e) => {
        const preview = e.preview as { kind: string } | undefined;
        return (
          <div key={e.id + (e.key ?? "")} className="sk-card">
            <Picture image={(e.thumbnail as Img) ?? null} />
            <strong>{e.title}</strong>
            <span className="muted small">
              {(e.date as { display: string } | null)?.display}
              {preview && <> · preview: {preview.kind}</>}
              {e.featured ? " · featured" : ""}
            </span>
            <p className="small">{e.summary}</p>
          </div>
        );
      })}
    </div>
  );
}

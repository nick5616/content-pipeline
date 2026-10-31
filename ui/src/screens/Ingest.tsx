import { useState, type DragEvent } from "react";
import { useOnEvents, useStore } from "../lib/store";
import { href } from "../lib/route";
import type { IngestBatch } from "../lib/types";

const STAGES = ["queued", "detect", "sanitize", "verify", "hash", "draft", "done"];

/**
 * Drop files to ingest them: each one is sanitized, verified and stored
 * locally, then drafted as an item. Nothing is uploaded until a plan is
 * applied.
 */
export function Ingest() {
  const { token, fetch } = useStore();
  const [batches, setBatches] = useState<IngestBatch[]>([]);
  const [over, setOver] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useOnEvents(["ingest"], (e) => {
    if (!e) return;
    const b = e.data as IngestBatch;
    setBatches((all) => all.map((x) => (x.id === b.id ? b : x)));
  });

  const send = async (files: File[]) => {
    if (!files.length) return;
    setError(null);
    const form = new FormData();
    files.forEach((f) => form.append("files", f, f.name));
    const res = await fetch("/api/ingest", { method: "POST", headers: { Authorization: `Bearer ${token}` }, body: form });
    const body = await res.json().catch(() => ({}));
    if (res.status === 202) setBatches((all) => [body as IngestBatch, ...all]);
    else setError(`${body.title ?? res.status}: ${body.detail ?? ""}`);
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    send([...e.dataTransfer.files]);
  };

  return (
    <section className="ingest">
      <label
        className={`dropzone${over ? " over" : ""}`}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={onDrop}
      >
        <strong>Drop files here</strong>
        <span className="muted">or click to choose. Images, video, audio and PDFs; location, device and owner metadata is stripped and checked.</span>
        <input type="file" multiple hidden onChange={(e) => send([...(e.target.files ?? [])])} />
      </label>
      {error && <p className="banner error">{error}</p>}
      {batches.map((b) => (
        <div key={b.id} className="batch">
          {b.files.map((f, i) => (
            <div key={i} className={`file stage-${f.stage}`}>
              <span className="name">{f.name}</span>
              <Stages stage={f.stage} />
              <span className="outcome">
                {f.stage === "failed" ? (
                  <span className="error">{f.error}</span>
                ) : f.stage === "done" && f.item ? (
                  <>
                    {f.note ? <span className="muted">{f.note} · </span> : null}
                    <a href={href({ screen: "item", id: f.item })}>edit the draft</a>
                  </>
                ) : (
                  <span className="muted">{f.detectedType ?? ""}</span>
                )}
              </span>
            </div>
          ))}
        </div>
      ))}
    </section>
  );
}

function Stages({ stage }: { stage: string }) {
  const at = STAGES.indexOf(stage);
  return (
    <ol className="stages" aria-label={`stage ${stage}`}>
      {STAGES.slice(1).map((s, i) => (
        <li key={s} className={stage === "failed" ? (i + 1 <= Math.max(at, 0) ? "passed" : "") : i + 1 < at ? "passed" : i + 1 === at ? "now" : ""}>
          {s}
        </li>
      ))}
    </ol>
  );
}

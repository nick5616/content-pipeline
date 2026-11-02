import { useEffect, useState } from "react";
import { useOnEvents, useStore } from "../lib/store";
import { thumbnailUrl, useAuthedImage } from "../lib/images";
import { href } from "../lib/route";
import { imageBlobs, type ApplyStatus, type FileDiff, type Item, type Op, type Plan, type Problem } from "../lib/types";

// How each op reads in the review, in execution order.
const GROUPS: { types: Op["type"][]; title: string; tone: string }[] = [
  { types: ["archive-original"], title: "Archive originals", tone: "add" },
  { types: ["upload-blob"], title: "Upload to the private bucket", tone: "add" },
  { types: ["run-job"], title: "Upload job outputs", tone: "add" },
  { types: ["publish-copy"], title: "Publish", tone: "public" },
  { types: ["write-manifest", "write-history"], title: "Private manifest", tone: "change" },
  { types: ["write-view", "write-public-manifest"], title: "Public views and manifest", tone: "public" },
  { types: ["unpublish"], title: "Unpublish", tone: "remove" },
  { types: ["orphan"], title: "Orphans (reported only; nothing is deleted)", tone: "muted" },
];

/** Make a plan, review it, and apply exactly that plan. */
export function PlanReview() {
  const { api, items } = useStore();
  const [plan, setPlan] = useState<Plan | null>(null);
  const [making, setMaking] = useState(false);
  const [error, setError] = useState<{ title: string; detail?: string; problems?: { severity: string; message: string; path: string }[] } | null>(null);
  const [status, setStatus] = useState<ApplyStatus | null>(null);

  useOnEvents(["apply"], (e) => {
    if (e && plan && e.id === plan.id) setStatus(e.data as ApplyStatus);
  }, [plan?.id]);

  const make = async () => {
    setMaking(true);
    setError(null);
    setStatus(null);
    const { data, error: err } = await api.POST("/api/plans");
    setMaking(false);
    if (data) setPlan(data);
    else {
      const p = err as Problem & { problems?: { severity: string; message: string; path: string }[] };
      setError({ title: p?.title ?? "Couldn't make a plan", detail: p?.detail, problems: p?.problems });
    }
  };

  const apply = async () => {
    if (!plan) return;
    setError(null);
    const { data, error: err, response } = await api.POST("/api/plans/{id}/apply", { params: { path: { id: plan.id } } });
    if (data) setStatus(data);
    else {
      const p = err as Problem | undefined;
      setError({
        title: p?.title ?? `Apply refused (${response.status})`,
        detail: response.status === 409 ? `${p?.detail ?? ""} Make the plan again.` : p?.detail,
      });
    }
  };

  const writes = plan?.ops.filter((o) => o.type !== "orphan") ?? [];
  const byId = new Map(items.map((i) => [i.id, i]));
  return (
    <section className="plan">
      <div className="toolbar">
        <button onClick={make} disabled={making || status?.state === "running"}>
          {making ? "Planning… (running pending jobs first)" : plan ? "Plan again" : "Make plan"}
        </button>
        {plan && (
          <span className="muted">
            Plan <code>{plan.id}</code> against generation {plan.generation}
          </span>
        )}
      </div>
      {error && (
        <div className="banner error">
          <strong>{error.title}</strong> {error.detail}
          {error.problems && (
            <ul className="problems">
              {error.problems.map((p, i) => (
                <li key={i} className={p.severity}>
                  <code>{p.path}</code> {p.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {!plan && !making && (
        <p className="muted">
          A plan compares your working manifest and local cache with the buckets. Nothing in the cloud changes until you apply it.
        </p>
      )}
      {plan && (
        <>
          <Summary plan={plan} />
          {writes.length === 0 ? (
            <p className="ok big">No changes. The buckets match the working manifest.</p>
          ) : (
            <div className="apply-bar">
              <button className="primary" onClick={apply} disabled={status?.state === "running" || status?.state === "done"}>
                Apply {writes.length} changes
              </button>
              <span className="muted small">The only button here that touches the cloud.</span>
              {status && <ApplyProgress status={status} />}
            </div>
          )}
          <Problems problems={plan.problems} />
          {GROUPS.map((g) => {
            const ops = plan.ops.filter((o) => g.types.includes(o.type));
            if (!ops.length) return null;
            return (
              <details key={g.title} className={`op-group tone-${g.tone}`} open={ops.length <= 40 && g.tone !== "muted"}>
                <summary>
                  {g.title} <span className="count">{ops.length}</span>
                </summary>
                <ul className="ops">
                  {ops.map((o, i) => (
                    <OpRow key={o.path + i} op={o} planId={plan.id} item={o.item ? byId.get(o.item) : undefined} />
                  ))}
                </ul>
              </details>
            );
          })}
        </>
      )}
    </section>
  );
}

/** Errors always show; more than a few warnings fold into one line. */
function Problems({ problems }: { problems: Plan["problems"] }) {
  if (problems.length === 0) return null;
  const errors = problems.filter((p) => p.severity === "error");
  const warnings = problems.filter((p) => p.severity !== "error");
  const list = (ps: Plan["problems"]) => (
    <ul className="problems">
      {ps.map((p, i) => (
        <li key={i} className={p.severity}>
          {p.message}
        </li>
      ))}
    </ul>
  );
  return (
    <>
      {errors.length > 0 && list(errors)}
      {warnings.length > 5 ? (
        <details className="warnings">
          <summary>{warnings.length} warnings</summary>
          {list(warnings)}
        </details>
      ) : (
        warnings.length > 0 && list(warnings)
      )}
    </>
  );
}

function Summary({ plan }: { plan: Plan }) {
  const s = plan.summary;
  const parts: [string, number][] = [
    ["to archive", s.archive],
    ["to upload", s.upload],
    ["jobs", s.jobs],
    ["to publish", s.publish],
    ["to unpublish", s.unpublish],
    ["orphaned", s.orphaned],
  ];
  return (
    <p className="summary">
      {parts.map(([label, n]) => (
        <span key={label} className={n ? "" : "muted"}>
          <strong>{n}</strong> {label}
        </span>
      ))}
    </p>
  );
}

// Ops that write a JSON document: their change can be shown as a diff.
const DIFFABLE: Op["type"][] = ["write-manifest", "write-view", "write-public-manifest"];

function OpRow({ op, planId, item }: { op: Op; planId: string; item?: Item }) {
  const { token } = useStore();
  const [open, setOpen] = useState(false);
  const blob = /blobs\/sha256\/..\/..\/([0-9a-f]{64})\./.exec(op.path)?.[1] ?? (item ? imageBlobs(item)[0] : undefined);
  const src = useAuthedImage(blob ? thumbnailUrl(blob, 96) : null, token);
  const diffable = DIFFABLE.includes(op.type);
  return (
    <li className="op-wrap">
      <div className="op">
        <span className="op-thumb">{src && <img src={src} alt="" />}</span>
        <span className="op-type">{op.type}</span>
        <code className="op-path">{short(op.path)}</code>
        <span className="op-detail">
          {item ? <a href={href({ screen: "item", id: item.id })}>{item.title}</a> : op.detail}
          {op.bytes != null && <span className="muted"> · {bytes(op.bytes)}</span>}
          {diffable && (
            <button type="button" className="link diff-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
              {open ? "Hide diff" : "Show diff"}
            </button>
          )}
        </span>
      </div>
      {open && <DiffView planId={planId} path={op.path} />}
    </li>
  );
}

/** The JSON in the bucket now against what this plan writes. */
function DiffView({ planId, path }: { planId: string; path: string }) {
  const { api } = useStore();
  const [diff, setDiff] = useState<FileDiff | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    api.GET("/api/plans/{id}/diff", { params: { path: { id: planId }, query: { path } } }).then(({ data, error: err }) => {
      if (!live) return;
      if (data) setDiff(data);
      else setError((err as Problem | undefined)?.detail ?? "Couldn't load the diff");
    });
    return () => {
      live = false;
    };
  }, [api, planId, path]);
  if (error) return <p className="diff error">{error}</p>;
  if (!diff) return <p className="diff muted">Loading diff…</p>;
  return (
    <div className="diff">
      <p className="diff-head">
        <span className="add">+{diff.added}</span> <span className="remove">−{diff.removed}</span>
        <span className="muted">
          {" "}
          {diff.exists ? `against the ${diff.bucket} bucket now` : `new in the ${diff.bucket} bucket`}
          {diff.rough && " · too different to align line by line"}
        </span>
      </p>
      {diff.hunks.length === 0 ? (
        <p className="muted">No line changes.</p>
      ) : (
        <div className="diff-body">
          {diff.hunks.map((h, i) => (
            <table key={i} className="hunk">
              <tbody>
                <tr className="hunk-head">
                  <td colSpan={3}>
                    @@ −{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@
                  </td>
                </tr>
                {numbered(h).map((l, j) => (
                  <tr key={j} className={l.op === "+" ? "add" : l.op === "-" ? "remove" : ""}>
                    <td className="ln">{l.old ?? ""}</td>
                    <td className="ln">{l.new ?? ""}</td>
                    <td className="code">
                      {l.op}
                      {l.text}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ))}
        </div>
      )}
    </div>
  );
}

/** A hunk's lines with their old and new line numbers. */
function numbered(h: FileDiff["hunks"][number]) {
  let o = h.oldStart;
  let n = h.newStart;
  return h.lines.map((l) => ({
    ...l,
    old: l.op === "+" ? undefined : o++,
    new: l.op === "-" ? undefined : n++,
  }));
}

function ApplyProgress({ status }: { status: ApplyStatus }) {
  const pct = status.total ? Math.round((100 * status.done) / status.total) : 0;
  return (
    <div className={`progress state-${status.state}`}>
      <div className="track">
        <div className="fill" style={{ width: `${pct}%` }} />
      </div>
      <span>
        {status.state === "done"
          ? `Applied. The remote manifest is now generation ${status.newGeneration ?? "unchanged"}.`
          : status.state === "failed"
            ? `Stopped after ${status.done} of ${status.total}: ${status.error}. What's done is safe; plan again to see what's left.`
            : `${status.done} of ${status.total}`}
      </span>
    </div>
  );
}

function short(p: string) {
  return p.replace(/([0-9a-f]{12})[0-9a-f]{52}/g, "$1…");
}

function bytes(n: number) {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

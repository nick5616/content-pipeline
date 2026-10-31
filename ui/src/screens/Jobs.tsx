import { useCallback, useEffect, useState } from "react";
import { useOnEvents, useStore } from "../lib/store";
import type { Job, JobRun } from "../lib/types";

/** The derived jobs on this machine: run them, and retry failures. */
export function Jobs() {
  const { api } = useStore();
  const [state, setState] = useState<"" | Job["state"]>("");
  const [jobs, setJobs] = useState<Job[]>([]);
  const [run, setRun] = useState<JobRun | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    const { data } = await api.GET("/api/jobs", { params: { query: state ? { state } : {} } });
    if (data) setJobs(data);
    const r = await api.GET("/api/jobs/run");
    if (r.data) setRun(r.data);
  }, [api, state]);

  useOnEvents(["job"], () => load(), [load]);
  useEffect(() => {
    load();
  }, [load]);

  const start = async () => {
    setError(null);
    const { data, error: err } = await api.POST("/api/jobs/run", { params: { query: {} } });
    if (data) setRun(data);
    else setError((err as { detail?: string })?.detail ?? "Couldn't start");
  };
  const retry = async (key: string) => {
    await api.POST("/api/jobs/{id}/retry", { params: { path: { id: key } } });
    load();
  };

  const failed = jobs.filter((j) => j.state === "failed").length;
  return (
    <section className="jobs">
      <div className="toolbar">
        <button onClick={start} disabled={run?.state === "running"}>
          {run?.state === "running" ? "Running…" : "Run jobs"}
        </button>
        <select value={state} onChange={(e) => setState(e.target.value as Job["state"])} aria-label="State">
          <option value="">Every state</option>
          <option value="pending">Pending</option>
          <option value="running">Running</option>
          <option value="done">Done</option>
          <option value="failed">Failed</option>
        </select>
        <span className="muted">
          {jobs.length} jobs{failed ? ` · ${failed} failed` : ""}
          {run?.report && run.state !== "running" && (
            <>
              {" "}
              · last run: {run.report.ran} ran, {run.report.cached} already here, {run.report.remote} in the bucket, {run.report.failed} failed
            </>
          )}
        </span>
      </div>
      {error && <p className="banner error">{error}</p>}
      <table className="table">
        <thead>
          <tr>
            <th>Recipe</th>
            <th>State</th>
            <th>Source</th>
            <th>Attempts</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {jobs.map((j) => (
            <tr key={j.jobkey} className={`state-${j.state}`}>
              <td>{j.recipe}</td>
              <td>
                <span className={`tag state-${j.state}`}>{j.state}</span>
              </td>
              <td>
                <code>{j.source.slice(7, 19)}</code>
                {j.lastError && <pre className="tool-error">{j.lastError}</pre>}
              </td>
              <td>{j.attempts}</td>
              <td>{j.state === "failed" && <button onClick={() => retry(j.jobkey)}>Retry</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

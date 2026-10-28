import { useEffect, useState } from "react";
import { createApi } from "./api";
import { takeToken } from "./token";

type Health = { state: "checking" } | { state: "ok" } | { state: "error"; message: string };

/** M0 placeholder: proves the token handshake and the API are wired up. */
export function App() {
  const [token] = useState(takeToken);
  const [health, setHealth] = useState<Health>({ state: "checking" });

  useEffect(() => {
    if (!token) return;
    let live = true;
    createApi(token)
      .GET("/api/health")
      .then(({ data, error, response }) => {
        if (!live) return;
        if (data?.status === "ok") setHealth({ state: "ok" });
        else setHealth({ state: "error", message: `${response.status} ${error?.title ?? response.statusText}` });
      })
      .catch((e: unknown) => live && setHealth({ state: "error", message: String(e) }));
    return () => {
      live = false;
    };
  }, [token]);

  return (
    <main>
      <h1>Canon</h1>
      {!token ? (
        <p className="error">No token. Open the URL that <code>canon serve</code> printed.</p>
      ) : health.state === "checking" ? (
        <p>Connecting…</p>
      ) : health.state === "ok" ? (
        <p className="ok">Connected to the local API.</p>
      ) : (
        <p className="error">API error: {health.message}</p>
      )}
      <p className="muted">The editor arrives in M6.</p>
    </main>
  );
}

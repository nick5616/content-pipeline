import { useEffect, useState } from "react";
import { useStore } from "../lib/store";
import type { DoctorReport } from "../lib/types";

/** canon doctor: tools, credentials, buckets, dirs. */
export function Doctor() {
  const { api } = useStore();
  const [r, setR] = useState<DoctorReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api.GET("/api/doctor").then(({ data, response }) => (data ? setR(data) : setError(String(response.status))));
  }, [api]);
  if (error) return <p className="error">Couldn't run the doctor ({error}).</p>;
  if (!r) return <p className="muted">Checking…</p>;
  return (
    <section className="doctor">
      <table className="table">
        <tbody>
          {r.checks.map((c) => (
            <tr key={c.name}>
              <td>
                <span className={`tag check-${c.status}`}>{c.status}</span>
              </td>
              <td>{c.name}</td>
              <td className="muted">{c.detail}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

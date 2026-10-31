import type { CanonEvent } from "./types";

/**
 * Reads GET /api/events with fetch (EventSource can't send the bearer
 * header) and calls onEvent for each one, reconnecting with backoff until
 * the signal aborts. onOpen runs on every (re)connect, so callers can
 * refetch what they might have missed.
 */
export async function streamEvents(
  token: string,
  onEvent: (e: CanonEvent) => void,
  signal: AbortSignal,
  onOpen: () => void = () => {},
  fetchImpl: typeof fetch = globalThis.fetch,
) {
  let delay = 500;
  while (!signal.aborted) {
    try {
      const res = await fetchImpl("/api/events", { headers: { Authorization: `Bearer ${token}` }, signal });
      if (!res.ok || !res.body) throw new Error(String(res.status));
      onOpen();
      delay = 500;
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let cut;
        while ((cut = buf.indexOf("\n\n")) >= 0) {
          const block = buf.slice(0, cut);
          buf = buf.slice(cut + 2);
          for (const line of block.split("\n")) {
            if (line.startsWith("data: ")) {
              try {
                onEvent(JSON.parse(line.slice(6)) as CanonEvent);
              } catch {
                // A malformed event is skipped, not fatal.
              }
            }
          }
        }
      }
    } catch {
      if (signal.aborted) return;
    }
    await new Promise((r) => setTimeout(r, delay));
    delay = Math.min(delay * 2, 10_000);
  }
}

import createClient from "openapi-fetch";
import type { paths } from "./gen/api";

export type Api = ReturnType<typeof createApi>;

/** A typed client for the local API, sending the bearer token on every call. */
export function createApi(token: string, fetchImpl: typeof fetch = globalThis.fetch) {
  // The page's own origin: what a relative URL means in the browser, and an
  // absolute one for runtimes whose Request needs it (tests).
  const client = createClient<paths>({ baseUrl: globalThis.location?.origin ?? "", fetch: fetchImpl });
  client.use({
    onRequest({ request }) {
      request.headers.set("Authorization", `Bearer ${token}`);
      return request;
    },
  });
  return client;
}

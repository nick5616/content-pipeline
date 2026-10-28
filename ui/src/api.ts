import createClient from "openapi-fetch";
import type { paths } from "./gen/api";

export type Api = ReturnType<typeof createApi>;

/** A typed client for the local API, sending the bearer token on every call. */
export function createApi(token: string, fetchImpl: typeof fetch = globalThis.fetch) {
  const client = createClient<paths>({ baseUrl: "", fetch: fetchImpl });
  client.use({
    onRequest({ request }) {
      request.headers.set("Authorization", `Bearer ${token}`);
      return request;
    },
  });
  return client;
}

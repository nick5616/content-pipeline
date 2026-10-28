import { describe, expect, it } from "vitest";
import { createApi } from "./api";

describe("createApi", () => {
  it("sends the bearer token and types the response", async () => {
    let auth: string | null = null;
    const fake = (async (req: Request) => {
      auth = req.headers.get("Authorization");
      return new Response(JSON.stringify({ status: "ok" }), { headers: { "Content-Type": "application/json" } });
    }) as typeof fetch;
    const { data } = await createApi("t0k3n", fake).GET("/api/health", { baseUrl: "http://127.0.0.1:4747" });
    expect(auth).toBe("Bearer t0k3n");
    expect(data?.status).toBe("ok");
  });
});

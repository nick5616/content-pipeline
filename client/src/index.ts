// @canon/client: typed loaders for Canon surface views. Sites read their own
// view and never the raw manifest. Every load is validated, so a schema or
// version mismatch fails loudly instead of rendering garbage.
import type { z } from "zod";
import { viewSchemas } from "./gen/views.js";

export { viewSchemas } from "./gen/views.js";
export type * from "./gen/views.js";

export type Surface = keyof typeof viewSchemas;
export type ViewFor<S extends Surface> = z.infer<(typeof viewSchemas)[S]>;

/** The viewVersion this client understands, per surface. */
export const VIEW_VERSION = 1;

export class ViewError extends Error {
  constructor(
    readonly surface: string,
    message: string,
    readonly issues: readonly z.core.$ZodIssue[] = [],
  ) {
    super(`@canon/client: ${surface}: ${message}`);
    this.name = "ViewError";
  }
}

export function isSurface(s: string): s is Surface {
  return Object.hasOwn(viewSchemas, s);
}

/** Validates already-parsed JSON as the view for `surface`. */
export function parseView<S extends Surface>(surface: S, data: unknown): ViewFor<S> {
  if (!isSurface(surface)) throw new ViewError(surface, "not a registered surface");
  const header = data as { view?: unknown; viewVersion?: unknown } | null;
  if (header && typeof header === "object") {
    if (header.view !== undefined && header.view !== surface) {
      throw new ViewError(surface, `got the view for ${JSON.stringify(header.view)}`);
    }
    if (header.viewVersion !== undefined && header.viewVersion !== VIEW_VERSION) {
      throw new ViewError(
        surface,
        `viewVersion ${JSON.stringify(header.viewVersion)} is not ${VIEW_VERSION}; upgrade @canon/client`,
      );
    }
  }
  const result = viewSchemas[surface].safeParse(data);
  if (!result.success) {
    const lines = result.error.issues.slice(0, 5).map((i) => `  ${i.path.join(".") || "(root)"}: ${i.message}`);
    const more = result.error.issues.length > 5 ? `\n  ...and ${result.error.issues.length - 5} more` : "";
    throw new ViewError(surface, `view does not match its schema:\n${lines.join("\n")}${more}`, result.error.issues);
  }
  return result.data as ViewFor<S>;
}

export interface LoadOptions {
  /** Public bucket base URL, e.g. https://storage.googleapis.com/<bucket>. */
  baseUrl: string;
  fetch?: typeof globalThis.fetch;
}

/** Fetches views/<surface>.json from the public bucket and validates it. */
export async function loadView<S extends Surface>(surface: S, opts: LoadOptions): Promise<ViewFor<S>> {
  const f = opts.fetch ?? globalThis.fetch;
  const url = `${opts.baseUrl.replace(/\/+$/, "")}/views/${surface}.json`;
  const res = await f(url);
  if (!res.ok) throw new ViewError(surface, `GET ${url}: ${res.status} ${res.statusText}`);
  let data: unknown;
  try {
    data = await res.json();
  } catch (e) {
    throw new ViewError(surface, `GET ${url}: not JSON (${String(e)})`);
  }
  return parseView(surface, data);
}

/** Reads a view from disk, for baking it in at build time (Node only). */
export async function loadViewFromFile<S extends Surface>(surface: S, path: string): Promise<ViewFor<S>> {
  const { readFile } = await import("node:fs/promises");
  return parseView(surface, JSON.parse(await readFile(path, "utf8")));
}

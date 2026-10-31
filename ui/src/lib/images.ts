import { useEffect, useState } from "react";

// An <img> can't send the bearer token, so local images are fetched with
// it and shown through object URLs. They're content-addressed, so each URL
// is fetched once per page load.
const cache = new Map<string, Promise<string | null>>();

export function authedImage(url: string, token: string, fetchImpl: typeof fetch = globalThis.fetch) {
  let p = cache.get(url);
  if (!p) {
    p = fetchImpl(url, { headers: { Authorization: `Bearer ${token}` } })
      .then((r) => (r.ok ? r.blob() : null))
      .then((b) => (b ? URL.createObjectURL(b) : null))
      .catch(() => null);
    cache.set(url, p);
  }
  return p;
}

/** The object URL for a local image, or null while loading or missing. */
export function useAuthedImage(url: string | null, token: string): string | null {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setSrc(null);
    if (url) authedImage(url, token).then((s) => live && setSrc(s));
    return () => {
      live = false;
    };
  }, [url, token]);
  return src;
}

export const thumbnailUrl = (sha: string, w = 320) => `/api/local/thumbnails/${sha}?w=${w}`;

/**
 * Maps a public-bucket URL in a view to the local cache, so previews show
 * files the next apply hasn't published yet.
 */
export function localize(publicUrl: string): string | null {
  const d = /\/derived\/([0-9a-f]{64})\/([^/?#]+)$/.exec(publicUrl);
  if (d) return `/api/local/derived/${d[1]}/${d[2]}`;
  const b = /\/blobs\/sha256\/[0-9a-f]{2}\/[0-9a-f]{2}\/([0-9a-f]{64})\.[a-z0-9]+$/.exec(publicUrl);
  if (b) return `/api/local/blobs/${b[1]}`;
  return null;
}

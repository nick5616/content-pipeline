const KEY = "canon.token";

/** Reads `#token=...` from a URL fragment. Returns null if absent or empty. */
export function tokenFromHash(hash: string): string | null {
  const params = new URLSearchParams(hash.replace(/^#/, ""));
  const t = params.get("token");
  return t ? t : null;
}

/**
 * Takes the token from the fragment (then removes it from the address bar so
 * it doesn't linger in history or screenshots), falling back to the one this
 * tab already stored.
 */
export function takeToken(): string | null {
  const fromHash = tokenFromHash(window.location.hash);
  if (fromHash) {
    try {
      sessionStorage.setItem(KEY, fromHash);
    } catch {
      // Storage blocked: the token still works for this page load.
    }
    history.replaceState(null, "", window.location.pathname + window.location.search);
    return fromHash;
  }
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

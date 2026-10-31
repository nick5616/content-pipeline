import { useEffect, useState } from "react";

/** The screens, as hash routes: #/items/<id>, #/plan, ... */
export type Route =
  | { screen: "library" }
  | { screen: "item"; id: string }
  | { screen: "plan" }
  | { screen: "ingest" }
  | { screen: "jobs" }
  | { screen: "surfaces"; id?: string }
  | { screen: "doctor" };

export function parseRoute(hash: string): Route {
  const [, screen, id] = hash.replace(/^#/, "").split("/");
  switch (screen) {
    case "items":
      return id ? { screen: "item", id: decodeURIComponent(id) } : { screen: "library" };
    case "plan":
    case "ingest":
    case "jobs":
    case "doctor":
      return { screen };
    case "surfaces":
      return { screen: "surfaces", id: id ? decodeURIComponent(id) : undefined };
    default:
      return { screen: "library" };
  }
}

export function href(r: Route): string {
  switch (r.screen) {
    case "library":
      return "#/";
    case "item":
      return `#/items/${encodeURIComponent(r.id)}`;
    case "surfaces":
      return r.id ? `#/surfaces/${encodeURIComponent(r.id)}` : "#/surfaces";
    default:
      return `#/${r.screen}`;
  }
}

export function go(r: Route) {
  window.location.hash = href(r);
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}

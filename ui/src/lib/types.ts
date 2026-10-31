import type { components } from "../gen/api";

type S = components["schemas"];
export type Item = S["item"];
export type Placement = S["placement"];
export type CopyFields = S["copyFields"];
export type Taxonomy = S["taxonomy"];
export type Term = S["taxonomyTerm"];
export type Link = S["link"];
export type Credit = S["credit"];
export type SurfaceInfo = S["SurfaceInfo"];
export type SyncStatus = S["SyncStatus"];
export type Plan = S["Plan"];
export type Op = Plan["ops"][number];
export type ApplyStatus = S["ApplyStatus"];
export type IngestBatch = S["IngestBatch"];
export type Job = S["Job"];
export type JobRun = S["JobRun"];
export type DoctorReport = S["DoctorReport"];
export type SurfaceChanges = S["SurfaceChanges"];
export type Problem = S["Problem"];
export type ValidationProblem = S["ValidationProblem"];
export type CanonEvent = { type: "ingest" | "job" | "apply" | "sync"; id: string; data: unknown };

/** The vocabularies an item points into. */
export const VOCABS = ["categories", "tech", "mediums", "tags"] as const;

/** An item's image blobs (sha256 hex), in asset order. */
export function imageBlobs(it: Item): string[] {
  return (it.assets ?? [])
    .filter((a) => ["original", "thumbnail", "cover", "screenshot"].includes(a.role))
    .map((a) => a.blob.replace(/^sha256:/, ""));
}

import { useState } from "react";

// Each surface's placement config schema, from schema/surfaces/<id>/. They're
// small (booleans, enums, URL lists, numbers, one or two nested objects), so
// a form is generated from them, with raw JSON for anything else. The engine
// validates every save against the same files.
const modules = import.meta.glob("../../../schema/surfaces/*/placement.schema.json", { eager: true, import: "default" });

type Schema = {
  type?: string | string[];
  enum?: string[];
  properties?: Record<string, Schema>;
  items?: Schema;
  anyOf?: Schema[];
  description?: string;
  required?: string[];
  minimum?: number;
  maximum?: number;
};

export const PLACEMENT_SCHEMAS: Record<string, Schema> = Object.fromEntries(
  Object.entries(modules).map(([path, schema]) => [path.split("/").at(-2)!, schema as Schema]),
);

/** A new placement's config: every required field at its first allowed value. */
export function defaultConfig(surface: string): Record<string, unknown> {
  const s = PLACEMENT_SCHEMAS[surface];
  const out: Record<string, unknown> = {};
  for (const k of s?.required ?? []) {
    const p = s?.properties?.[k];
    out[k] = p?.enum?.[0] ?? (p?.type === "boolean" ? false : "");
  }
  return out;
}

type Value = Record<string, unknown>;

export function PlacementForm({
  surface,
  value,
  onChange,
}: {
  surface: string;
  value: Value;
  onChange: (v: Value) => void;
}) {
  const schema = PLACEMENT_SCHEMAS[surface];
  const [raw, setRaw] = useState(false);
  if (!schema?.properties || raw) {
    return <RawJson value={value} onChange={onChange} onForm={schema?.properties ? () => setRaw(false) : undefined} />;
  }
  return (
    <div className="placement-form">
      <ObjectFields schema={schema} value={value} onChange={onChange} />
      <button type="button" className="link" onClick={() => setRaw(true)}>
        Edit as JSON
      </button>
    </div>
  );
}

function ObjectFields({ schema, value, onChange }: { schema: Schema; value: Value; onChange: (v: Value) => void }) {
  const set = (k: string, v: unknown) => {
    const next = { ...value };
    if (v === undefined || v === "") delete next[k];
    else next[k] = v;
    onChange(next);
  };
  return (
    <>
      {Object.entries(schema.properties ?? {}).map(([k, p]) => (
        <Field key={k} name={k} schema={p} required={schema.required?.includes(k)} value={value[k]} onChange={(v) => set(k, v)} />
      ))}
    </>
  );
}

function Field({
  name,
  schema,
  value,
  required,
  onChange,
}: {
  name: string;
  schema: Schema;
  value: unknown;
  required?: boolean;
  onChange: (v: unknown) => void;
}) {
  const label = (
    <span className="field-label" title={schema.description}>
      {name}
      {required && " *"}
    </span>
  );
  if (schema.enum) {
    return (
      <label className="field">
        {label}
        <select value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || undefined)}>
          {!required && <option value="">(not set)</option>}
          {schema.enum.map((o) => (
            <option key={o}>{o}</option>
          ))}
        </select>
      </label>
    );
  }
  if (schema.anyOf) {
    // pinned: true, or a dock position (D62).
    const shown = value === true ? "yes" : value === undefined || value === false ? "" : String(value);
    return (
      <label className="field">
        {label}
        <input
          value={shown}
          placeholder="yes, a position (0, 1, ...) or blank"
          onChange={(e) => {
            const t = e.target.value.trim();
            onChange(t === "" ? undefined : t === "yes" || t === "true" ? true : /^\d+$/.test(t) ? Number(t) : t);
          }}
        />
      </label>
    );
  }
  switch (schema.type) {
    case "boolean":
      return (
        <label className="field inline">
          <input type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked || undefined)} />
          {label}
        </label>
      );
    case "integer":
    case "number":
      return (
        <label className="field">
          {label}
          <input
            type="number"
            min={schema.minimum}
            max={schema.maximum}
            value={(value as number | undefined) ?? ""}
            onChange={(e) => onChange(e.target.value === "" ? undefined : Number(e.target.value))}
          />
        </label>
      );
    case "array":
      return (
        <label className="field">
          {label}
          <textarea
            rows={2}
            placeholder="one per line"
            value={((value as string[]) ?? []).join("\n")}
            onChange={(e) => {
              const lines = e.target.value.split("\n").map((l) => l.trim()).filter(Boolean);
              onChange(lines.length ? lines : undefined);
            }}
          />
        </label>
      );
    case "object":
      return (
        <fieldset className="field nested">
          <legend title={schema.description}>{name}</legend>
          <ObjectFields
            schema={schema}
            value={(value as Value) ?? {}}
            onChange={(v) => onChange(Object.keys(v).length ? v : undefined)}
          />
        </fieldset>
      );
    default:
      return (
        <label className="field">
          {label}
          <input value={(value as string) ?? ""} onChange={(e) => onChange(e.target.value || undefined)} />
        </label>
      );
  }
}

function RawJson({ value, onChange, onForm }: { value: Value; onChange: (v: Value) => void; onForm?: () => void }) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 2));
  const [bad, setBad] = useState(false);
  return (
    <div className="placement-form">
      <textarea
        className="mono"
        rows={4}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          try {
            const v = JSON.parse(e.target.value);
            setBad(false);
            if (v && typeof v === "object" && !Array.isArray(v)) onChange(v);
          } catch {
            setBad(true);
          }
        }}
      />
      {bad && <span className="error">Not valid JSON yet; nothing saved.</span>}
      {onForm && (
        <button type="button" className="link" onClick={onForm}>
          Back to the form
        </button>
      )}
    </div>
  );
}

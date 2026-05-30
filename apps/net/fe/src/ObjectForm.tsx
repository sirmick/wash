// Generic descriptor-driven object editor — the "Advanced" tier for every model
// object (docs/NET.md §6, A6). It is a thin rendering shell over the pure
// buildForm() model; all logic (union switching, advanced filtering,
// diagnostics) lives there and is unit-tested. Runtime behaviour is exercised by
// the Phase-B wash-vm/vm e2e harness.

import { For, Show } from "solid-js";
import {
  buildForm,
  type Diagnostic,
  type FieldView,
  type ObjectDescriptor,
} from "./objectform-model.ts";

export interface ObjectFormProps {
  object: ObjectDescriptor;
  value: Record<string, any>;
  diagnostics?: Diagnostic[];
  showAdvanced?: boolean;
  pathPrefix: string;
  /** Resolve an i18n labelKey to a display string. */
  label?: (key: string) => string;
  /** Candidate values for a ref picker of the given kind. */
  refOptions?: (kind: string) => string[];
  onChange?: (path: string, value: unknown) => void;
}

export function ObjectForm(props: ObjectFormProps) {
  const form = () =>
    buildForm(props.object, props.value, props.diagnostics ?? [], {
      showAdvanced: !!props.showAdvanced,
      pathPrefix: props.pathPrefix,
    });
  return (
    <div class="wash-net-form">
      <For each={form().groups}>
        {(g) => (
          <fieldset class="wash-net-group">
            <legend>{g.name}</legend>
            <For each={g.fields}>{(f) => <Field view={f} props={props} />}</For>
          </fieldset>
        )}
      </For>
    </div>
  );
}

function Field(p: { view: FieldView; props: ObjectFormProps }) {
  const f = p.view;
  const label = () => p.props.label?.(f.field.labelKey) ?? f.field.name;
  const set = (v: unknown) => p.props.onChange?.(f.path, v);
  return (
    <label class="wash-net-field" classList={{ error: f.severity === "error" }}>
      <span class="wash-net-label">{label()}</span>
      <Widget view={f} props={p.props} set={set} />
      <Show when={f.error}>
        <span class="wash-net-diag" data-severity={f.severity}>{f.error}</span>
      </Show>
    </label>
  );
}

function Widget(p: { view: FieldView; props: ObjectFormProps; set: (v: unknown) => void }) {
  const f = p.view;
  const w = f.field.widget;

  if (f.union) {
    return (
      <div class="wash-net-union">
        <select
          value={f.union.tag}
          onChange={(e) => p.set({ _tag: e.currentTarget.value })}
        >
          <For each={f.union.options}>{(t) => <option value={t}>{t}</option>}</For>
        </select>
        <For each={f.union.fields}>{(sf) => <Field view={sf} props={p.props} />}</For>
      </div>
    );
  }

  if (w === "ref") {
    const opts = () => p.props.refOptions?.(f.field.ref ?? "") ?? [];
    return (
      <Show
        when={!f.field.list}
        fallback={<RefList view={f} options={opts()} set={p.set} />}
      >
        <select value={String(f.value ?? "")} onChange={(e) => p.set(e.currentTarget.value)}>
          <option value="" />
          <For each={opts()}>{(o) => <option value={o}>{o}</option>}</For>
        </select>
      </Show>
    );
  }

  if (w.startsWith("list-")) {
    const arr = Array.isArray(f.value) ? (f.value as unknown[]) : [];
    return (
      <textarea
        rows={Math.max(2, arr.length)}
        value={arr.join("\n")}
        onChange={(e) => p.set(e.currentTarget.value.split("\n").map((s) => s.trim()).filter(Boolean))}
      />
    );
  }

  switch (w) {
    case "toggle":
      return <input type="checkbox" checked={!!f.value} onChange={(e) => p.set(e.currentTarget.checked)} />;
    case "number":
      return <input type="number" value={String(f.value ?? "")} onInput={(e) => p.set(Number(e.currentTarget.value))} />;
    case "password":
      return <input type="password" value={String(f.value ?? "")} onInput={(e) => p.set(e.currentTarget.value)} />;
    default:
      // text, ip, cidr, mac, section-name — plain text with the widget as a hint
      return (
        <input
          type="text"
          data-widget={w}
          value={String(f.value ?? "")}
          onInput={(e) => p.set(e.currentTarget.value)}
        />
      );
  }
}

function RefList(p: { view: FieldView; options: string[]; set: (v: unknown) => void }) {
  const selected = () => (Array.isArray(p.view.value) ? (p.view.value as string[]) : []);
  const toggle = (o: string, on: boolean) => {
    const cur = new Set(selected());
    on ? cur.add(o) : cur.delete(o);
    p.set([...cur]);
  };
  return (
    <div class="wash-net-reflist">
      <For each={p.options}>
        {(o) => (
          <label>
            <input type="checkbox" checked={selected().includes(o)} onChange={(e) => toggle(o, e.currentTarget.checked)} />
            {o}
          </label>
        )}
      </For>
    </div>
  );
}

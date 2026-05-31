// Generic descriptor-driven object editor — the schema-driven editor for every
// model object (docs/NET.md §6, A6). It is a thin rendering shell over the pure
// buildForm() model; all logic (union switching, diagnostics) lives there and is
// unit-tested. Runtime behaviour is exercised by the wash-vm/vm e2e harness.
//
// Rendering uses <Index> (not <For>) keyed by position, and every leaf reads its
// FieldView through an accessor: buildForm() returns fresh objects on every
// keystroke, so a reference-keyed <For> would destroy + recreate each input and
// the field being typed in would lose focus. <Index> keeps the DOM stable by
// position and only the bound values update — focus is preserved.
//
// A union (e.g. the addressing "IPv4 method") renders FLAT: a full-width method
// <select> followed by the active variant's fields as SIBLING rows, so they line
// up with the rest of the form instead of being indented inside the union's cell.

import { Index, Match, Show, Switch } from "solid-js";
import type { Accessor } from "solid-js";
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
      pathPrefix: props.pathPrefix,
    });
  return (
    <div class="wash-net-form">
      <Index each={form().groups}>
        {(g) => (
          <div class="wash-net-group">
            {/* No bordered "general" panel — only label a genuinely-named group. */}
            <Show when={g().name && g().name !== "general"}>
              <div class="wash-net-grouplabel">{g().name}</div>
            </Show>
            <Index each={g().fields}>{(f) => <FieldRow view={f} props={props} />}</Index>
          </div>
        )}
      </Index>
    </div>
  );
}

function FieldRow(p: { view: Accessor<FieldView>; props: ObjectFormProps }) {
  return (
    <Show when={p.view().union} fallback={<Field view={p.view} props={p.props} />}>
      <UnionField view={p.view} props={p.props} />
    </Show>
  );
}

// UnionField renders the discriminator <select> as its own full-width row (the
// option labels are self-describing, so no redundant field label), then the
// active variant's fields as plain sibling rows — aligned with everything else.
function UnionField(p: { view: Accessor<FieldView>; props: ObjectFormProps }) {
  const f = () => p.view();
  const set = (v: unknown) => p.props.onChange?.(f().path, v);
  return (
    <>
      <div class="wash-net-method">
        <select value={f().union!.tag} onChange={(e) => set({ _tag: e.currentTarget.value })}>
          <Index each={f().union!.options}>
            {(t) => <option value={t()}>{p.props.label?.("variant." + t()) ?? t()}</option>}
          </Index>
        </select>
      </div>
      <Index each={f().union!.fields}>{(sf) => <FieldRow view={sf} props={p.props} />}</Index>
    </>
  );
}

function Field(p: { view: Accessor<FieldView>; props: ObjectFormProps }) {
  const label = () => p.props.label?.(p.view().field.labelKey) ?? p.view().field.name;
  const set = (v: unknown) => p.props.onChange?.(p.view().path, v);
  return (
    <label class="wash-net-field" classList={{ error: p.view().severity === "error" }}>
      <span class="wash-net-label">{label()}</span>
      <BasicWidget view={p.view} props={p.props} set={set} />
      <Show when={p.view().error}>
        <span class="wash-net-diag" data-severity={p.view().severity}>{p.view().error}</span>
      </Show>
    </label>
  );
}

function BasicWidget(p: { view: Accessor<FieldView>; props: ObjectFormProps; set: (v: unknown) => void }) {
  const f = () => p.view();
  const w = () => f().field.widget;
  return (
    <Switch
      fallback={
        // text, ip, cidr, mac, section-name — plain text; the widget is a hint.
        <input
          type="text"
          data-widget={w()}
          value={String(f().value ?? "")}
          onInput={(e) => p.set(e.currentTarget.value)}
        />
      }
    >
      <Match when={w() === "ref"}>
        <RefWidget view={p.view} props={p.props} set={p.set} />
      </Match>
      <Match when={w().startsWith("list-")}>
        <textarea
          rows={Math.max(2, (Array.isArray(f().value) ? (f().value as unknown[]) : []).length)}
          value={(Array.isArray(f().value) ? (f().value as unknown[]) : []).join("\n")}
          onChange={(e) => p.set(e.currentTarget.value.split("\n").map((s) => s.trim()).filter(Boolean))}
        />
      </Match>
      <Match when={w() === "toggle"}>
        <input type="checkbox" checked={!!f().value} onChange={(e) => p.set(e.currentTarget.checked)} />
      </Match>
      <Match when={w() === "number"}>
        <input type="number" value={String(f().value ?? "")} onInput={(e) => p.set(Number(e.currentTarget.value))} />
      </Match>
      <Match when={w() === "password"}>
        <input type="password" value={String(f().value ?? "")} onInput={(e) => p.set(e.currentTarget.value)} />
      </Match>
    </Switch>
  );
}

function RefWidget(p: { view: Accessor<FieldView>; props: ObjectFormProps; set: (v: unknown) => void }) {
  const f = () => p.view();
  const opts = () => p.props.refOptions?.(f().field.ref ?? "") ?? [];
  return (
    <Show when={!f().field.list} fallback={<RefList view={p.view} options={opts} set={p.set} />}>
      <select value={String(f().value ?? "")} onChange={(e) => p.set(e.currentTarget.value)}>
        <option value="" />
        <Index each={opts()}>{(o) => <option value={o()}>{o()}</option>}</Index>
      </select>
    </Show>
  );
}

function RefList(p: { view: Accessor<FieldView>; options: () => string[]; set: (v: unknown) => void }) {
  const selected = () => (Array.isArray(p.view().value) ? (p.view().value as string[]) : []);
  const toggle = (o: string, on: boolean) => {
    const cur = new Set(selected());
    on ? cur.add(o) : cur.delete(o);
    p.set([...cur]);
  };
  return (
    <div class="wash-net-reflist">
      <Index each={p.options()}>
        {(o) => (
          <label>
            <input type="checkbox" checked={selected().includes(o())} onChange={(e) => toggle(o(), e.currentTarget.checked)} />
            {o()}
          </label>
        )}
      </Index>
    </div>
  );
}

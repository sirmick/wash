// Pure descriptor-interpretation layer for the generic ObjectForm (docs/NET.md
// §6, A6). It turns a UI descriptor object + a value + server diagnostics into a
// grouped view-model the Solid shell renders. No framework imports — this is the
// unit-tested core (the widget choice already lives in the descriptor; here we
// resolve groups, the active union variant, and the diagnostic→field mapping).
// Every field always shows — there is no basic/advanced split.

export interface FieldDescriptor {
  name: string;
  option: string;
  widget: string;
  group?: string;
  ref?: string;
  list?: boolean;
  labelKey: string;
  union?: UnionDescriptor;
}
export interface UnionDescriptor {
  discriminator: string;
  variants: VariantDescriptor[];
}
export interface VariantDescriptor {
  tag: string;
  goType: string;
  fields: FieldDescriptor[];
}
export interface ObjectDescriptor {
  kind: string;
  package: string;
  section: string;
  goType: string;
  fields: FieldDescriptor[];
}
export interface Descriptor {
  objects: ObjectDescriptor[];
}

export type Severity = "error" | "warning";
export interface Diagnostic {
  path: string;
  code: string;
  message: string;
  // The BE marshals "error"/"warning"; tolerate the int form (0/1) too.
  severity: Severity | number;
}

export interface FieldView {
  field: FieldDescriptor;
  path: string;
  value: unknown;
  error?: string;
  severity?: Severity;
  // Present iff the field is a union: the active discriminator tag, the
  // selectable tags, and the active variant's sub-fields (already filtered).
  union?: { tag: string; options: string[]; fields: FieldView[] };
}
export interface GroupView {
  name: string;
  fields: FieldView[];
}
export interface FormView {
  groups: GroupView[];
}

export interface BuildOpts {
  // Object path used to match diagnostics, e.g. "Interfaces[0]".
  pathPrefix: string;
}

export function descriptorFor(d: Descriptor, kind: string): ObjectDescriptor | undefined {
  return d.objects.find((o) => o.kind === kind);
}

// buildForm produces the grouped view-model. Union values follow the convention
// value[fieldName] = { _tag, ...variantFields }.
export function buildForm(
  obj: ObjectDescriptor,
  value: Record<string, any> | undefined,
  diags: Diagnostic[],
  opts: BuildOpts,
): FormView {
  const byPath = indexDiags(diags);
  const groups: GroupView[] = [];
  const index = new Map<string, number>();
  const group = (name: string): GroupView => {
    let i = index.get(name);
    if (i === undefined) {
      i = groups.length;
      index.set(name, i);
      groups.push({ name, fields: [] });
    }
    return groups[i];
  };

  for (const f of obj.fields) {
    // Join without a leading dot when there's no prefix (e.g. the addressing
    // fragment uses pathPrefix=""), so paths are "Proto" not ".Proto".
    const path = opts.pathPrefix ? `${opts.pathPrefix}.${f.name}` : f.name;
    const fv: FieldView = { field: f, path, value: value?.[f.name] };
    attachError(fv, byPath);

    if (f.union) {
      const uval = (value?.[f.name] ?? {}) as Record<string, any>;
      const tag = typeof uval._tag === "string" ? uval._tag : "";
      const variant = f.union.variants.find((v) => v.tag === tag);
      const subFields: FieldView[] = [];
      if (variant) {
        // A fieldless variant (e.g. NoneProto) serializes with fields: null in
        // the descriptor — guard so iterating it doesn't throw. A throw here is
        // especially nasty: it happens inside the reactive form() computation, so
        // Solid disposes the whole scope and the form freezes on every later edit.
        for (const sf of variant.fields ?? []) {
          const sfv: FieldView = { field: sf, path: `${path}.${sf.name}`, value: uval[sf.name] };
          attachError(sfv, byPath);
          subFields.push(sfv);
        }
      }
      fv.union = { tag, options: f.union.variants.map((v) => v.tag), fields: subFields };
    }

    group(f.group || "general").fields.push(fv);
  }
  return { groups };
}

function indexDiags(diags: Diagnostic[]): Map<string, Diagnostic> {
  const m = new Map<string, Diagnostic>();
  for (const d of diags) {
    const prev = m.get(d.path);
    // Prefer an error over a warning for the same path.
    if (!prev || (sev(prev) === "warning" && sev(d) === "error")) m.set(d.path, d);
  }
  return m;
}

function sev(d: Diagnostic): Severity {
  return d.severity === 1 || d.severity === "warning" ? "warning" : "error";
}

function attachError(fv: FieldView, byPath: Map<string, Diagnostic>): void {
  const d = byPath.get(fv.path);
  if (d) {
    fv.error = d.message;
    fv.severity = sev(d);
  }
}

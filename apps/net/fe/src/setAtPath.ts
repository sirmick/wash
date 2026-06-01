// setAtPath immutably sets a dotted path on a plain object. A union switch
// arrives as the bare discriminator path with a {_tag} value, which replaces
// the whole variant (dropping the old variant's fields). Pure + framework-
// free so the path-routing — historically a bug source (edits landing under
// a stray key when the path was computed wrong) — is unit-testable directly
// rather than only through the net e2e.
export function setAtPath(
  obj: Record<string, any>,
  keys: string[],
  v: unknown,
): Record<string, any> {
  const [head, ...rest] = keys;
  const next = { ...obj };
  next[head] = rest.length === 0 ? v : setAtPath((obj[head] as Record<string, any>) ?? {}, rest, v);
  return next;
}

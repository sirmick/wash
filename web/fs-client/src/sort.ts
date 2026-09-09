// Directory listing sort + hidden-file filter. Framework-free (no DOM,
// no Solid) and pure: the comparator and dotfile filter were previously
// reachable only through fm's column-sorting browser e2e; here they get
// millisecond node:test coverage. Shared with edit (it lists files too),
// so it lives in @wash/fs-client.
//
// Extracted verbatim from fm's App() closure.

export type SortKey = 'name' | 'mtime' | 'ctime' | 'size' | 'type';

// The entry fields the sort/filter reads. fm/edit's richer Entry types
// are structurally assignable. `type` names the ENTRY KIND ('dir',
// 'file', 'symlink'); "sort by type" means the user-facing kind of a
// file — its extension — not that field.
export interface SortableEntry {
  name: string;
  type: string;
  size: number;
  mod_unix: number;
  created_unix: number;
}

export interface SortOptions {
  key: SortKey;
  desc: boolean;
  showHidden: boolean;
}

// extensionOf is the sort-by-type axis for a file: the lowercased final
// suffix, without the dot. A name with no dot, or a leading-dot name with
// no other dot (".bashrc"), has no extension and sorts before the ones
// that do — the same place "no kind" belongs in a kind-ordered list.
export function extensionOf(name: string): string {
  const i = name.lastIndexOf('.');
  if (i <= 0 || i === name.length - 1) return '';
  return name.slice(i + 1).toLowerCase();
}

// sortedFiltered returns a NEW array (input untouched): dotfiles dropped
// unless showHidden, then sorted. Directories sort before files for every
// key INCLUDING 'type': "sort by type" groups by the kind of thing, and a
// folder is not a kind of file. Within the files, 'type' orders by
// extension and breaks ties by name. Names compare case-insensitively;
// `desc` flips the final comparison. Generic so callers get their own
// entry type back.
export function sortedFiltered<E extends SortableEntry>(
  entries: readonly E[],
  opts: SortOptions,
): E[] {
  let out = entries.slice();
  if (!opts.showHidden) out = out.filter((e) => !e.name.startsWith('.'));
  const { key, desc } = opts;
  out.sort((a, b) => {
    if (a.type === 'dir' && b.type !== 'dir') return -1;
    if (a.type !== 'dir' && b.type === 'dir') return 1;
    let cmp = 0;
    switch (key) {
      case 'name':
        cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
        break;
      case 'mtime':
        cmp = a.mod_unix - b.mod_unix;
        break;
      case 'ctime':
        cmp = a.created_unix - b.created_unix;
        break;
      case 'size':
        cmp = a.size - b.size;
        break;
      case 'type':
        // Dirs are already grouped above, so this is file-vs-file (or
        // dir-vs-dir, where neither has an extension and it falls
        // straight through to the name).
        cmp = extensionOf(a.name).localeCompare(extensionOf(b.name));
        if (cmp === 0) cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
        break;
    }
    return desc ? -cmp : cmp;
  });
  return out;
}

// Directory listing sort + hidden-file filter. Framework-free (no DOM,
// no Solid) and pure: the comparator and dotfile filter were previously
// reachable only through fm's column-sorting browser e2e; here they get
// millisecond node:test coverage. Shared with edit (it lists files too),
// so it lives in @wash/fs-client.
//
// Extracted verbatim from fm's App() closure.

export type SortKey = 'name' | 'mtime' | 'ctime' | 'size' | 'type';

// The entry fields the sort/filter reads. fm/edit's richer Entry types
// are structurally assignable. `type` is compared as a plain string and
// the value 'dir' is special-cased for dir-before-file grouping.
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

// sortedFiltered returns a NEW array (input untouched): dotfiles dropped
// unless showHidden, then sorted. Directories sort before files for every
// key EXCEPT 'type' (where the type field itself is the sort axis). Names
// compare case-insensitively; a 'type' tie breaks by name. `desc` flips
// the final comparison. Generic so callers get their own entry type back.
export function sortedFiltered<E extends SortableEntry>(
  entries: readonly E[],
  opts: SortOptions,
): E[] {
  let out = entries.slice();
  if (!opts.showHidden) out = out.filter((e) => !e.name.startsWith('.'));
  const { key, desc } = opts;
  out.sort((a, b) => {
    if (key !== 'type') {
      if (a.type === 'dir' && b.type !== 'dir') return -1;
      if (a.type !== 'dir' && b.type === 'dir') return 1;
    }
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
        cmp = a.type.localeCompare(b.type);
        if (cmp === 0) cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
        break;
    }
    return desc ? -cmp : cmp;
  });
  return out;
}

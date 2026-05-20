// wash-app-fm: single-tree + preview file manager.
// - Left pane: hierarchical tree, rooted at /, auto-expanded to $HOME
//   on first list_ok. Click triangle to toggle; click row name to
//   select. Selecting a folder also auto-expands it.
// - Right pane: file preview (text or "binary").
// - Below preview: collapsible info section (perms, size, mtime,
//   symlink target if relevant).
// - Toolbar: Home, Back, Up, editable path (Enter to navigate),
//   Reload, Sort dropdown (sort key + show-hidden toggle).
// - Right-click on a row: Open · Copy path · Show info.
// v1 is read-only. Future: rename/delete/symlink (v2),
// move/copy (v3), drag-and-drop, autocomplete, clipboard service.

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
    };
  }
}

interface Entry {
  name: string;
  type: 'dir' | 'file' | 'symlink' | 'other';
  size: number;
  mod_unix: number;
  perm: string;
  mode: number;
  link_to?: string;
  link_err?: string;
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

type SortKey = 'name' | 'mtime' | 'size' | 'type';

const HOME_FALLBACK = '/';

class WashAppFM extends HTMLElement {
  private instance = '';
  private home = HOME_FALLBACK;

  // ---- state ----
  private listings = new Map<string, Entry[]>(); // path → entries
  private expanded = new Set<string>();
  private selectedPath = '';
  private selectedEntry: Entry | null = null;
  private history: string[] = [];
  private historyIdx = -1;
  private sortKey: SortKey = 'name';
  private sortDesc = false;
  private showHidden = false;
  private infoOpen = false;
  private rootInitialized = false;
  // pending paths we are awaiting from the BE (so a list response can
  // be linked to a click that triggered it).
  private pendingNav: string | null = null;
  // Double-click detection keyed by path. renderTree replaces row
  // elements on every state change, so per-row closures would be
  // discarded between the two clicks; the state lives on the host.
  private lastClickPath = '';
  private lastClickTime = 0;
  // Autocomplete state (path bar dropdown).
  private completeDropdown: HTMLDivElement | null = null;
  private completeMatches: string[] = [];
  private completeIdx = -1;
  private completeTimer: number | null = null;
  private completePartial = '';

  // ---- dom refs ----
  private treeEl!: HTMLDivElement;
  private previewEl!: HTMLDivElement;
  private infoBody!: HTMLDivElement;
  private infoToggleEl!: HTMLButtonElement;
  private statusEl!: HTMLDivElement;
  private pathInput!: HTMLInputElement;
  private sortBtn!: HTMLButtonElement;
  private menuEl: HTMLDivElement | null = null;

  // ---- lifecycle ----

  connectedCallback() {
    this.instance = this.getAttribute('data-wash-instance') ?? '';
    this.style.cssText = [
      'display:grid',
      'grid-template-rows:36px 1fr 22px',
      'width:100%',
      'height:100%',
      'background:#10101a',
      'color:#eee',
      'font:13px ui-sans-serif,system-ui,sans-serif',
      'box-sizing:border-box',
      'position:relative', // anchor absolute-positioned overlays (menus, dropdowns)
    ].join(';');

    this.appendChild(this.buildToolbar());
    this.appendChild(this.buildBody());
    this.appendChild(this.buildStatus());

    this.addEventListener('wash:msg', (ev) => {
      this.handleBE((ev as CustomEvent).detail as BEMessage);
    });
    // Close any open context menu when clicking elsewhere.
    document.addEventListener('mousedown', (ev) => {
      if (this.menuEl && !this.menuEl.contains(ev.target as Node)) {
        this.closeMenu();
      }
    });
    // Ctrl-C on the selected row copies its path to the wash clipboard.
    this.addEventListener('keydown', (ev) => {
      if ((ev.ctrlKey || ev.metaKey) && ev.key === 'c' && this.selectedPath && document.activeElement !== this.pathInput) {
        ev.preventDefault();
        window.wash.sendAppMsg(this.instance, { kind: 'clipboard_copy_path', path: this.selectedPath });
      }
    });
    // The host needs focus to receive keydown reliably; tabindex makes it focusable.
    if (!this.hasAttribute('tabindex')) {
      this.tabIndex = 0;
    }
  }

  // ---- DOM construction ----

  private buildToolbar(): HTMLElement {
    const bar = document.createElement('div');
    bar.style.cssText = [
      'display:flex',
      'align-items:center',
      'gap:4px',
      'padding:0 8px',
      'background:#181828',
    ].join(';');

    const home = iconBtn('fm-home', '⌂', 'Home');
    home.addEventListener('click', () => this.goHome());
    bar.appendChild(home);

    const back = iconBtn('fm-back', '←', 'Back');
    back.addEventListener('click', () => this.goBack());
    bar.appendChild(back);

    const up = iconBtn('fm-up', '↑', 'Up');
    up.addEventListener('click', () => this.goUp());
    bar.appendChild(up);

    this.pathInput = document.createElement('input');
    this.pathInput.type = 'text';
    this.pathInput.dataset.testid = 'fm-path';
    this.pathInput.spellcheck = false;
    this.pathInput.placeholder = 'path';
    this.pathInput.style.cssText = [
      'flex:1',
      'background:#10101a',
      'color:#eee',
      'border:1px solid #2a2a3a',
      'border-radius:3px',
      'padding:4px 8px',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
      'outline:none',
    ].join(';');
    this.pathInput.addEventListener('keydown', (ev) => this.onPathKey(ev));
    this.pathInput.addEventListener('input', () => this.onPathInput());
    this.pathInput.addEventListener('blur', () => {
      // Defer close so a mousedown on a dropdown item can fire first.
      setTimeout(() => this.closeCompleteDropdown(), 100);
    });
    bar.appendChild(this.pathInput);

    const reload = iconBtn('fm-reload', '↻', 'Reload');
    reload.addEventListener('click', () => {
      if (this.selectedPath) this.invalidateAndList(parentPath(this.selectedPath));
    });
    bar.appendChild(reload);

    this.sortBtn = iconBtn('fm-sort', '⇅', 'Sort');
    this.sortBtn.addEventListener('click', (ev) => this.openSortMenu(ev));
    bar.appendChild(this.sortBtn);

    return bar;
  }

  private buildBody(): HTMLElement {
    const grid = document.createElement('div');
    grid.style.cssText = [
      'display:grid',
      'grid-template-columns:1fr 1fr',
      'overflow:hidden',
      'border-top:1px solid #2a2a3a',
      'border-bottom:1px solid #2a2a3a',
    ].join(';');

    this.treeEl = document.createElement('div');
    this.treeEl.dataset.testid = 'fm-list';
    this.treeEl.style.cssText = [
      'overflow:auto',
      'background:#181828',
      'padding:4px 0',
      'border-right:1px solid #2a2a3a',
    ].join(';');
    grid.appendChild(this.treeEl);

    const right = document.createElement('div');
    right.style.cssText = 'display:grid;grid-template-rows:1fr auto;overflow:hidden;';
    this.previewEl = document.createElement('div');
    this.previewEl.dataset.testid = 'fm-preview';
    this.previewEl.style.cssText = [
      'overflow:auto',
      'padding:10px 12px',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
      'white-space:pre',
      'background:#0c0c14',
    ].join(';');
    this.previewEl.textContent = '(select a file to preview)';
    right.appendChild(this.previewEl);
    right.appendChild(this.buildInfoSection());
    grid.appendChild(right);

    return grid;
  }

  private buildInfoSection(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.style.cssText = 'border-top:1px solid #2a2a3a;background:#15152a;';
    this.infoToggleEl = document.createElement('button');
    this.infoToggleEl.type = 'button';
    this.infoToggleEl.dataset.testid = 'fm-info-toggle';
    this.infoToggleEl.textContent = '▸ Info';
    this.infoToggleEl.style.cssText = [
      'display:block',
      'width:100%',
      'text-align:left',
      'background:transparent',
      'color:#eee',
      'border:none',
      'padding:6px 12px',
      'cursor:pointer',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
    ].join(';');
    this.infoToggleEl.addEventListener('click', () => this.toggleInfo());
    wrap.appendChild(this.infoToggleEl);
    this.infoBody = document.createElement('div');
    this.infoBody.dataset.testid = 'fm-info';
    this.infoBody.style.cssText = [
      'display:none',
      'padding:8px 12px 12px',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
      'border-top:1px solid #2a2a3a',
    ].join(';');
    wrap.appendChild(this.infoBody);
    return wrap;
  }

  private buildStatus(): HTMLElement {
    this.statusEl = document.createElement('div');
    this.statusEl.dataset.testid = 'fm-status';
    this.statusEl.style.cssText = [
      'padding:0 10px',
      'font:11px ui-monospace,Menlo,Consolas,monospace',
      'opacity:0.6',
      'display:flex',
      'align-items:center',
    ].join(';');
    this.statusEl.textContent = 'loading…';
    return this.statusEl;
  }

  // ---- BE comms ----

  private sendList(path: string) {
    this.pendingNav = path;
    window.wash.sendAppMsg(this.instance, { kind: 'list', path });
  }

  private sendRead(path: string) {
    this.previewEl.dataset.path = path;
    this.previewEl.textContent = 'loading…';
    window.wash.sendAppMsg(this.instance, { kind: 'read', path });
  }

  private handleBE(m: BEMessage) {
    switch (m.kind) {
      case 'list_ok': {
        const path = String(m.path);
        const entries = m.entries as Entry[];
        this.listings.set(path, entries);
        this.expanded.add(path);
        if (!this.rootInitialized) {
          this.rootInitialized = true;
          this.home = path;
          this.selectedPath = path;
          this.selectedEntry = null;
          this.history = [path];
          this.historyIdx = 0;
          this.expandPath(path);
        }
        this.statusEl.textContent = `${entries.length} entries${m.truncated ? ' (truncated)' : ''}`;
        this.pendingNav = null;
        // If a pending selection was waiting on this path's parent
        // listing, resolve it now.
        if (this.pendingSelectAfter) {
          const par = parentPath(this.pendingSelectAfter.path);
          if (par === path) {
            const p = this.pendingSelectAfter;
            this.pendingSelectAfter = null;
            this.selectPath(p.path, p.pushHistory);
            return;
          }
        }
        this.renderTree();
        this.updatePathInput();
        this.renderInfo();
        return;
      }
      case 'list_err':
        this.statusEl.textContent = `error: ${String(m.msg)}`;
        this.pendingNav = null;
        return;
      case 'read_ok':
        this.renderPreview(m as unknown as { path: string; content: string; binary: boolean; size: number; truncated: boolean });
        return;
      case 'read_err':
        this.previewEl.textContent = `error: ${String(m.msg)}`;
        return;
      case 'complete_ok': {
        const partial = String(m.partial);
        const matches = (m.matches as string[]) ?? [];
        // Ignore late responses for a query we no longer care about.
        if (partial !== this.completePartial) return;
        this.completeMatches = matches;
        this.completeIdx = matches.length > 0 ? 0 : -1;
        this.renderCompleteDropdown();
        return;
      }
    }
  }

  // ---- autocomplete ----

  private onPathInput() {
    const value = this.pathInput.value;
    if (this.completeTimer != null) {
      window.clearTimeout(this.completeTimer);
    }
    this.completeTimer = window.setTimeout(() => {
      this.completePartial = value;
      window.wash.sendAppMsg(this.instance, { kind: 'complete', partial: value });
    }, 120);
  }

  private onPathKey(ev: KeyboardEvent) {
    if (this.completeDropdown && this.completeMatches.length > 0) {
      if (ev.key === 'ArrowDown') {
        ev.preventDefault();
        this.completeIdx = (this.completeIdx + 1) % this.completeMatches.length;
        this.renderCompleteDropdown();
        return;
      }
      if (ev.key === 'ArrowUp') {
        ev.preventDefault();
        this.completeIdx =
          (this.completeIdx - 1 + this.completeMatches.length) % this.completeMatches.length;
        this.renderCompleteDropdown();
        return;
      }
      if (ev.key === 'Tab') {
        ev.preventDefault();
        this.pickCompletion(this.completeIdx, false);
        return;
      }
      if (ev.key === 'Escape') {
        ev.preventDefault();
        this.closeCompleteDropdown();
        return;
      }
      if (ev.key === 'Enter') {
        ev.preventDefault();
        this.pickCompletion(this.completeIdx, true);
        return;
      }
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      this.navigateTo(this.pathInput.value);
    } else if (ev.key === 'Escape') {
      this.closeCompleteDropdown();
    }
  }

  private pickCompletion(idx: number, alsoNavigate: boolean) {
    if (idx < 0 || idx >= this.completeMatches.length) return;
    const pick = this.completeMatches[idx];
    this.pathInput.value = pick;
    this.closeCompleteDropdown();
    if (alsoNavigate) {
      // For directories the picker added a trailing "/"; navigate
      // either way (the BE accepts both).
      this.navigateTo(pick.endsWith('/') ? pick.slice(0, -1) : pick);
    }
  }

  private renderCompleteDropdown() {
    // Drop only the DOM element here — the data drives this render.
    if (this.completeDropdown) {
      this.completeDropdown.remove();
      this.completeDropdown = null;
    }
    if (this.completeMatches.length === 0) return;
    const drop = document.createElement('div');
    drop.dataset.testid = 'fm-complete';
    drop.style.cssText = [
      'position:absolute',
      'background:#15152a',
      'border:1px solid #2a2a3a',
      'border-radius:4px',
      'padding:2px 0',
      'min-width:240px',
      'max-height:280px',
      'overflow-y:auto',
      'box-shadow:0 8px 20px rgba(0,0,0,0.5)',
      'z-index:1500',
      'font:12px ui-monospace,Menlo,Consolas,monospace',
    ].join(';');
    const rect = this.pathInput.getBoundingClientRect();
    const my = this.getBoundingClientRect();
    drop.style.left = `${rect.left - my.left}px`;
    drop.style.top = `${rect.bottom - my.top + 2}px`;
    drop.style.width = `${rect.width}px`;
    this.completeMatches.forEach((match, i) => {
      const row = document.createElement('div');
      row.dataset.testid = `fm-complete-${i}`;
      row.textContent = match;
      row.style.cssText = [
        'padding:4px 8px',
        'cursor:pointer',
        'color:#eee',
        `background:${i === this.completeIdx ? '#23234a' : 'transparent'}`,
        'white-space:nowrap',
        'overflow:hidden',
        'text-overflow:ellipsis',
      ].join(';');
      row.addEventListener('mouseenter', () => {
        this.completeIdx = i;
        this.renderCompleteDropdown();
      });
      // Use mousedown so it fires before the input's blur handler
      // closes the dropdown.
      row.addEventListener('mousedown', (ev) => {
        ev.preventDefault();
        this.pickCompletion(i, true);
      });
      drop.appendChild(row);
    });
    this.completeDropdown = drop;
    this.appendChild(drop);
  }

  // closeCompleteDropdown drops the DOM AND the candidate list — used
  // after picking, on Escape, on blur. While typing, only the DOM is
  // removed by renderCompleteDropdown so the next response can append
  // afresh without racing with the data we just received.
  private closeCompleteDropdown() {
    if (this.completeDropdown) {
      this.completeDropdown.remove();
      this.completeDropdown = null;
    }
    this.completeMatches = [];
    this.completeIdx = -1;
  }

  // ---- navigation / state ----

  private goHome() {
    this.navigateTo(this.home);
  }

  private goBack() {
    if (this.historyIdx > 0) {
      this.historyIdx -= 1;
      const path = this.history[this.historyIdx];
      this.selectPath(path, false);
    }
  }

  private goUp() {
    if (!this.selectedPath) return;
    const p = parentPath(this.selectedPath);
    if (p === this.selectedPath) return;
    this.navigateTo(p);
  }

  private navigateTo(path: string) {
    const target = path || '/';
    this.selectPath(target, true);
  }

  // selectPath sets the selection, ensures listings up to that path
  // are available (requests any that aren't cached), and re-renders.
  private selectPath(path: string, pushHistory: boolean) {
    const parent = parentPath(path);
    if (!this.listings.has(parent) && parent !== path) {
      // Request the parent listing first; the entry's metadata comes
      // from that.
      this.sendList(parent);
      this.pendingSelectAfter = { path, pushHistory };
      return;
    }
    const entry = this.findEntry(path);
    this.selectedPath = path;
    this.selectedEntry = entry;
    if (pushHistory) {
      this.history = this.history.slice(0, this.historyIdx + 1);
      this.history.push(path);
      this.historyIdx = this.history.length - 1;
    }
    if (entry?.type === 'dir' || (path === this.findRootPath() && this.listings.has(path))) {
      if (!this.listings.has(path)) {
        this.sendList(path);
        return;
      }
      this.expanded.add(path);
    } else if (entry?.type === 'symlink') {
      // single click: select; don't follow yet (double-click follows).
    } else if (entry?.type === 'file') {
      this.sendRead(path);
    }
    this.expandPath(path);
    this.renderTree();
    this.updatePathInput();
    this.renderInfo();
  }

  private pendingSelectAfter: { path: string; pushHistory: boolean } | null = null;

  private findRootPath(): string {
    // The tree is always rooted at /. Ancestors of the selected path
    // are added to `expanded`, so the user sees a path drilled down
    // from the filesystem root regardless of which subtree they're in.
    return '/';
  }

  private findEntry(path: string): Entry | null {
    const parent = parentPath(path);
    const name = baseName(path);
    const entries = this.listings.get(parent);
    if (!entries) return null;
    return entries.find((e) => e.name === name) ?? null;
  }

  // expandPath ensures every ancestor of `path` is marked expanded.
  // We request listings for any missing ones.
  private expandPath(path: string) {
    const parts = path.split('/').filter(Boolean);
    let acc = '/';
    this.expanded.add(acc);
    if (!this.listings.has(acc)) {
      this.sendList(acc);
    }
    for (const p of parts) {
      acc = acc === '/' ? '/' + p : acc + '/' + p;
      this.expanded.add(acc);
      if (!this.listings.has(acc)) {
        this.sendList(acc);
      }
    }
  }

  private invalidateAndList(path: string) {
    this.listings.delete(path);
    this.sendList(path);
  }

  private updatePathInput() {
    this.pathInput.value = this.selectedPath || '';
  }

  // ---- toggle on tree row ----

  private toggleExpand(path: string) {
    if (this.expanded.has(path)) {
      this.expanded.delete(path);
    } else {
      this.expanded.add(path);
      if (!this.listings.has(path)) {
        this.sendList(path);
        // We'll re-render when list_ok arrives.
      }
    }
    this.renderTree();
  }

  // ---- preview / info ----

  private renderPreview(m: { path: string; content: string; binary: boolean; size: number; truncated: boolean }) {
    if (m.binary) {
      this.previewEl.textContent = `(binary, ${humanSize(m.size)})`;
      return;
    }
    let text = m.content;
    if (m.truncated) {
      text += `\n\n--- truncated (${humanSize(m.size)} total) ---`;
    }
    this.previewEl.textContent = text;
  }

  private renderInfo() {
    this.infoBody.replaceChildren();
    if (!this.selectedEntry) {
      this.infoBody.textContent = '(no selection)';
      return;
    }
    const e = this.selectedEntry;
    const rows: Array<[string, string]> = [
      ['Path', this.selectedPath],
      ['Type', e.type],
      ['Size', humanSize(e.size)],
      ['Modified', new Date(e.mod_unix * 1000).toLocaleString()],
      ['Permissions', e.perm + ` (${octalPerm(e.mode)})`],
    ];
    if (e.type === 'symlink') {
      rows.push(['Link target', e.link_to ?? `(${e.link_err ?? 'unresolved'})`]);
    }
    for (const [k, v] of rows) {
      const row = document.createElement('div');
      row.style.cssText = 'display:flex;gap:10px;padding:2px 0;';
      const key = document.createElement('span');
      key.style.cssText = 'width:110px;opacity:0.6;flex-shrink:0;';
      key.textContent = k;
      const val = document.createElement('span');
      val.style.cssText = 'overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';
      val.textContent = v;
      row.appendChild(key);
      row.appendChild(val);
      this.infoBody.appendChild(row);
    }
  }

  private toggleInfo() {
    this.infoOpen = !this.infoOpen;
    this.infoBody.style.display = this.infoOpen ? 'block' : 'none';
    this.infoToggleEl.textContent = this.infoOpen ? '▾ Info' : '▸ Info';
  }

  // ---- sort menu ----

  private openSortMenu(anchorEv: MouseEvent) {
    this.closeMenu();
    const menu = document.createElement('div');
    menu.dataset.testid = 'fm-sort-menu';
    menu.style.cssText = menuBoxStyle();
    const sortItem = (key: SortKey, label: string) => {
      const it = menuItem(label + (this.sortKey === key ? (this.sortDesc ? ' ▾' : ' ▴') : ''));
      it.dataset.testid = `fm-sort-${key}`;
      it.addEventListener('click', () => {
        if (this.sortKey === key) {
          this.sortDesc = !this.sortDesc;
        } else {
          this.sortKey = key;
          this.sortDesc = false;
        }
        this.closeMenu();
        this.renderTree();
      });
      return it;
    };
    menu.appendChild(sortItem('name', 'Name'));
    menu.appendChild(sortItem('mtime', 'Modified'));
    menu.appendChild(sortItem('size', 'Size'));
    menu.appendChild(sortItem('type', 'Type'));
    const sep = document.createElement('div');
    sep.style.cssText = 'height:1px;background:#2a2a3a;margin:4px 0;';
    menu.appendChild(sep);
    const showHidden = menuItem(`${this.showHidden ? '☑' : '☐'} Show hidden`);
    showHidden.dataset.testid = 'fm-show-hidden';
    showHidden.addEventListener('click', () => {
      this.showHidden = !this.showHidden;
      this.closeMenu();
      this.renderTree();
    });
    menu.appendChild(showHidden);

    // Position near the button.
    const rect = (anchorEv.currentTarget as HTMLElement).getBoundingClientRect();
    const my = this.getBoundingClientRect();
    menu.style.left = `${rect.right - my.left - 160}px`;
    menu.style.top = `${rect.bottom - my.top + 4}px`;
    this.menuEl = menu;
    this.appendChild(menu);
  }

  // ---- context menu ----

  private openContextMenu(ev: MouseEvent, entry: Entry, path: string) {
    ev.preventDefault();
    this.closeMenu();
    // Right-click selects the row so Info / preview reflect the target.
    this.selectedEntry = entry;
    this.selectedPath = path;
    this.updatePathInput();
    this.renderInfo();
    if (entry.type === 'file') {
      this.sendRead(path);
    }
    const menu = document.createElement('div');
    menu.dataset.testid = 'fm-context-menu';
    menu.style.cssText = menuBoxStyle();
    const open = menuItem('Open');
    open.dataset.testid = 'fm-ctx-open';
    open.addEventListener('click', () => {
      this.closeMenu();
      if (entry.type === 'symlink') {
        this.followSymlink(entry, path);
      } else {
        this.selectPath(path, true);
      }
    });
    menu.appendChild(open);
    const copy = menuItem('Copy path');
    copy.dataset.testid = 'fm-ctx-copy';
    copy.addEventListener('click', () => {
      this.closeMenu();
      window.wash.sendAppMsg(this.instance, { kind: 'clipboard_copy_path', path });
    });
    menu.appendChild(copy);
    const info = menuItem('Show info');
    info.dataset.testid = 'fm-ctx-info';
    info.addEventListener('click', () => {
      this.closeMenu();
      if (!this.infoOpen) this.toggleInfo();
    });
    menu.appendChild(info);

    const my = this.getBoundingClientRect();
    menu.style.left = `${ev.clientX - my.left}px`;
    menu.style.top = `${ev.clientY - my.top}px`;
    this.menuEl = menu;
    this.appendChild(menu);
  }

  private closeMenu() {
    if (this.menuEl) {
      this.menuEl.remove();
      this.menuEl = null;
    }
  }

  private followSymlink(e: Entry, path: string) {
    if (!e.link_to) return;
    const target = e.link_to.startsWith('/') ? e.link_to : joinPath(parentPath(path), e.link_to);
    this.navigateTo(target);
  }

  // ---- tree render ----

  private renderTree() {
    this.treeEl.replaceChildren();
    const root = this.findRootPath();
    if (!this.listings.has(root)) return;
    this.renderNode(root, 0);
  }

  private renderNode(path: string, depth: number) {
    const entries = this.listings.get(path);
    if (!entries) return;
    const sorted = this.sortedFiltered(entries);
    for (const e of sorted) {
      const childPath = joinPath(path, e.name);
      this.treeEl.appendChild(this.renderRow(e, childPath, depth));
      if (e.type === 'dir' && this.expanded.has(childPath) && this.listings.has(childPath)) {
        this.renderNode(childPath, depth + 1);
      }
    }
  }

  private renderRow(e: Entry, path: string, depth: number): HTMLElement {
    const row = document.createElement('div');
    row.dataset.testid = `fm-entry-${e.name}`;
    row.dataset.type = e.type;
    row.dataset.path = path;
    const isSelected = this.selectedPath === path;
    row.style.cssText = [
      'display:flex',
      'align-items:center',
      'gap:4px',
      `padding:3px 8px 3px ${8 + depth * 16}px`,
      `background:${isSelected ? '#23234a' : 'transparent'}`,
      'color:#eee',
      'cursor:pointer',
      'user-select:none',
      'font:13px ui-sans-serif,system-ui,sans-serif',
    ].join(';');
    row.addEventListener('mouseenter', () => {
      if (this.selectedPath !== path) row.style.background = '#1d1d30';
    });
    row.addEventListener('mouseleave', () => {
      row.style.background = isSelected ? '#23234a' : 'transparent';
    });

    const triangle = document.createElement('span');
    triangle.style.cssText = 'width:12px;text-align:center;opacity:0.6;display:inline-block;cursor:pointer;';
    if (e.type === 'dir') {
      triangle.textContent = this.expanded.has(path) ? '▾' : '▸';
      triangle.addEventListener('click', (ev) => {
        ev.stopPropagation();
        this.toggleExpand(path);
      });
    } else {
      triangle.textContent = ' ';
    }
    row.appendChild(triangle);

    const icon = document.createElement('span');
    icon.style.cssText = 'width:14px;text-align:center;opacity:0.8;';
    icon.textContent = iconFor(e);
    row.appendChild(icon);

    const name = document.createElement('span');
    name.textContent = e.name;
    name.style.cssText = 'flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';
    row.appendChild(name);

    const size = document.createElement('span');
    size.style.cssText = 'opacity:0.5;font:11px ui-monospace,Menlo,Consolas,monospace;';
    size.textContent = e.type === 'file' ? humanSize(e.size) : '';
    row.appendChild(size);

    row.addEventListener('click', () => {
      const now = Date.now();
      const isDouble = path === this.lastClickPath && now - this.lastClickTime < 400;
      this.lastClickPath = path;
      this.lastClickTime = now;
      if (isDouble && e.type === 'symlink') {
        this.followSymlink(e, path);
        return;
      }
      this.selectPath(path, true);
    });
    row.addEventListener('contextmenu', (ev) => this.openContextMenu(ev, e, path));

    return row;
  }

  private sortedFiltered(entries: Entry[]): Entry[] {
    let out = entries;
    if (!this.showHidden) {
      out = out.filter((e) => !e.name.startsWith('.'));
    }
    out = out.slice();
    out.sort((a, b) => {
      // Folders before files unless sorting by type explicitly.
      if (this.sortKey !== 'type') {
        if (a.type === 'dir' && b.type !== 'dir') return -1;
        if (a.type !== 'dir' && b.type === 'dir') return 1;
      }
      let cmp = 0;
      switch (this.sortKey) {
        case 'name':
          cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
          break;
        case 'mtime':
          cmp = a.mod_unix - b.mod_unix;
          break;
        case 'size':
          cmp = a.size - b.size;
          break;
        case 'type':
          cmp = a.type.localeCompare(b.type);
          if (cmp === 0) cmp = a.name.toLowerCase().localeCompare(b.name.toLowerCase());
          break;
      }
      return this.sortDesc ? -cmp : cmp;
    });
    return out;
  }
}

// ---- helpers ----

function iconBtn(testid: string, label: string, title: string): HTMLButtonElement {
  const b = document.createElement('button');
  b.type = 'button';
  b.dataset.testid = testid;
  b.textContent = label;
  b.title = title;
  b.style.cssText = [
    'background:transparent',
    'color:#eee',
    'border:1px solid #2a2a3a',
    'border-radius:3px',
    'padding:4px 8px',
    'cursor:pointer',
    'font:13px ui-sans-serif,system-ui,sans-serif',
    'min-width:30px',
  ].join(';');
  return b;
}

function menuBoxStyle(): string {
  return [
    'position:absolute',
    'background:#15152a',
    'border:1px solid #2a2a3a',
    'border-radius:4px',
    'padding:4px 0',
    'min-width:160px',
    'box-shadow:0 6px 16px rgba(0,0,0,0.5)',
    'z-index:1000',
  ].join(';');
}

function menuItem(label: string): HTMLButtonElement {
  const b = document.createElement('button');
  b.type = 'button';
  b.textContent = label;
  b.style.cssText = [
    'display:block',
    'width:100%',
    'text-align:left',
    'background:transparent',
    'color:#eee',
    'border:none',
    'padding:6px 14px',
    'cursor:pointer',
    'font:13px ui-sans-serif,system-ui,sans-serif',
  ].join(';');
  b.addEventListener('mouseenter', () => {
    b.style.background = '#23233a';
  });
  b.addEventListener('mouseleave', () => {
    b.style.background = 'transparent';
  });
  return b;
}

function iconFor(e: Entry): string {
  switch (e.type) {
    case 'dir':
      return '▸';
    case 'symlink':
      return '↪';
    case 'file':
      return '·';
    default:
      return '?';
  }
}

function joinPath(parent: string, name: string): string {
  if (parent.endsWith('/')) return parent + name;
  return parent + '/' + name;
}

function parentPath(path: string): string {
  if (!path || path === '/') return '/';
  const i = path.lastIndexOf('/');
  if (i <= 0) return '/';
  return path.slice(0, i);
}

function baseName(path: string): string {
  const i = path.lastIndexOf('/');
  return i < 0 ? path : path.slice(i + 1);
}

function humanSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function octalPerm(mode: number): string {
  return '0' + (mode & 0o777).toString(8);
}

if (!customElements.get('wash-app-fm')) {
  customElements.define('wash-app-fm', WashAppFM);
}

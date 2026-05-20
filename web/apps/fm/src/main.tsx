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
// move/copy (v3).
//
// Solid drives the rendering — state mutations automatically re-run
// just the views that read them. No more "I changed a field but
// forgot to re-render" bugs.

import { For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore, produce } from 'solid-js/store';
import { render } from 'solid-js/web';
import type { Component, JSX } from 'solid-js';
import {
  ArrowLeft,
  ArrowUp,
  ArrowUpDown,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  File as FileIcon,
  Folder as FolderIcon,
  Home as HomeIcon,
  Link2,
  RotateCw,
  Square,
} from 'lucide-solid';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
    };
  }
}

interface PersistedState {
  path?: string;
  expanded?: string[];
  sort_key?: SortKey;
  sort_desc?: boolean;
  show_hidden?: boolean;
  info_open?: boolean;
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

type MenuState =
  | { kind: 'sort'; left: number; top: number }
  | { kind: 'context'; left: number; top: number; entry: Entry; path: string }
  | null;

const HOME_FALLBACK = '/';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  // ---- reactive state ----
  const [path, setPath] = createSignal('');
  const [selectedEntry, setSelectedEntry] = createSignal<Entry | null>(null);
  const [listings, setListings] = createStore<Record<string, Entry[]>>({});
  const [expanded, setExpanded] = createStore<Record<string, true>>({});
  const [history, setHistory] = createSignal<string[]>([]);
  const [historyIdx, setHistoryIdx] = createSignal(-1);
  const [sortKey, setSortKey] = createSignal<SortKey>('name');
  const [sortDesc, setSortDesc] = createSignal(false);
  const [showHidden, setShowHidden] = createSignal(false);
  const [infoOpen, setInfoOpen] = createSignal(false);
  const [home, setHome] = createSignal(HOME_FALLBACK);
  const [rootInitialized, setRootInitialized] = createSignal(false);
  const [pathInputValue, setPathInputValue] = createSignal('');
  const [previewContent, setPreviewContent] = createSignal<{ binary: boolean; size: number; text: string; truncated: boolean } | null>(null);
  // statusOverride is set by transient one-shot messages (drop, error).
  // While non-null it wins over the auto-derived visible-entry count.
  // Navigation / clicks clear it so the auto status resumes.
  const [statusOverride, setStatusOverride] = createSignal<string | null>(null);
  const [statusDropPath, setStatusDropPath] = createSignal('');
  const [menu, setMenu] = createSignal<MenuState>(null);

  // Autocomplete
  const [completeMatches, setCompleteMatches] = createSignal<string[]>([]);
  const [completeIdx, setCompleteIdx] = createSignal(-1);
  const [completeOpen, setCompleteOpen] = createSignal(false);

  // Refs / latched state (no reactivity needed)
  let pendingNav: string | null = null;
  let pendingSelectAfter: { path: string; pushHistory: boolean } | null = null;
  let completePartial = '';
  let completeTimer: number | null = null;
  let lastClickPath = '';
  let lastClickTime = 0;
  let pathInputEl!: HTMLInputElement;

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // ---- BE comms ----

  const sendList = (p: string) => {
    pendingNav = p;
    send({ kind: 'list', path: p });
  };
  const sendRead = (p: string) => {
    setPreviewContent({ binary: false, size: 0, text: 'loading…', truncated: false });
    send({ kind: 'read', path: p });
  };

  const handleBE = (m: BEMessage) => {
    switch (m.kind) {
      case 'list_ok': {
        const p = String(m.path);
        const entries = m.entries as Entry[];
        setListings(p, entries);
        setExpanded(p, true);
        if (!rootInitialized()) {
          setRootInitialized(true);
          setHome(p);
          setPath(p);
          setPathInputValue(p);
          setSelectedEntry(null);
          setHistory([p]);
          setHistoryIdx(0);
          expandPath(p);
        }
        pendingNav = null;
        if (pendingSelectAfter) {
          const par = parentPath(pendingSelectAfter.path);
          if (par === p) {
            const ps = pendingSelectAfter;
            pendingSelectAfter = null;
            selectPath(ps.path, ps.pushHistory);
          }
        }
        return;
      }
      case 'list_err':
        setStatusOverride(`error: ${String(m.msg)}`);
        pendingNav = null;
        return;
      case 'read_ok': {
        const r = m as unknown as { binary: boolean; size: number; content: string; truncated: boolean };
        setPreviewContent({ binary: r.binary, size: r.size, text: r.content, truncated: r.truncated });
        return;
      }
      case 'read_err':
        setPreviewContent({ binary: false, size: 0, text: `error: ${String(m.msg)}`, truncated: false });
        return;
      case 'complete_ok': {
        const partial = String(m.partial);
        const matches = (m.matches as string[]) ?? [];
        if (partial !== completePartial) return;
        setCompleteMatches(matches);
        setCompleteIdx(matches.length > 0 ? 0 : -1);
        setCompleteOpen(matches.length > 0);
        return;
      }
    }
  };

  // ---- navigation ----

  const findEntry = (p: string): Entry | null => {
    const par = parentPath(p);
    const name = baseName(p);
    const entries = listings[par];
    if (!entries) return null;
    return entries.find((e) => e.name === name) ?? null;
  };

  const expandPath = (p: string) => {
    const parts = p.split('/').filter(Boolean);
    let acc = '/';
    setExpanded(acc, true);
    if (!listings[acc]) sendList(acc);
    for (const part of parts) {
      acc = acc === '/' ? '/' + part : acc + '/' + part;
      setExpanded(acc, true);
      if (!listings[acc]) sendList(acc);
    }
  };

  const selectPath = (p: string, pushHistory: boolean) => {
    setStatusOverride(null);
    const par = parentPath(p);
    if (!listings[par] && par !== p) {
      sendList(par);
      pendingSelectAfter = { path: p, pushHistory };
      return;
    }
    const entry = findEntry(p);
    setPath(p);
    setSelectedEntry(entry);
    // Sync the path bar immediately. We do this before any early
    // returns for unloaded dir listings so the input always reflects
    // the currently-selected path, even while a list response is in
    // flight.
    setPathInputValue(p);
    if (pushHistory) {
      const h = history().slice(0, historyIdx() + 1);
      h.push(p);
      setHistory(h);
      setHistoryIdx(h.length - 1);
    }
    if (entry?.type === 'dir' || (p === '/' && listings[p])) {
      if (!listings[p]) {
        sendList(p);
        return;
      }
      setExpanded(p, true);
    } else if (entry?.type === 'file') {
      sendRead(p);
    }
    expandPath(p);
    persist();
  };

  const navigateTo = (p: string) => selectPath(p || '/', true);
  const goHome = () => navigateTo(home());
  const goBack = () => {
    if (historyIdx() > 0) {
      const newIdx = historyIdx() - 1;
      setHistoryIdx(newIdx);
      selectPath(history()[newIdx], false);
    }
  };
  const goUp = () => {
    if (!path()) return;
    const p = parentPath(path());
    if (p !== path()) navigateTo(p);
  };

  const invalidateAndList = (p: string) => {
    setListings(produce((s) => { delete s[p]; }));
    sendList(p);
  };

  const toggleExpand = (p: string) => {
    if (expanded[p]) {
      setExpanded(produce((s) => { delete s[p]; }));
    } else {
      setExpanded(p, true);
      if (!listings[p]) sendList(p);
    }
    persist();
  };

  const toggleInfo = () => {
    setInfoOpen(!infoOpen());
    persist();
  };

  const followSymlink = (e: Entry, p: string) => {
    if (!e.link_to) return;
    const target = e.link_to.startsWith('/') ? e.link_to : joinPath(parentPath(p), e.link_to);
    navigateTo(target);
  };

  // ---- autocomplete ----

  const onPathInput = (value: string) => {
    setPathInputValue(value);
    if (completeTimer != null) window.clearTimeout(completeTimer);
    completeTimer = window.setTimeout(() => {
      completePartial = value;
      send({ kind: 'complete', partial: value });
    }, 120);
  };

  const closeCompleteDropdown = () => {
    setCompleteOpen(false);
    setCompleteMatches([]);
    setCompleteIdx(-1);
  };

  const pickCompletion = (idx: number, alsoNavigate: boolean) => {
    const matches = completeMatches();
    if (idx < 0 || idx >= matches.length) return;
    const pick = matches[idx];
    setPathInputValue(pick);
    closeCompleteDropdown();
    if (alsoNavigate) {
      navigateTo(pick.endsWith('/') ? pick.slice(0, -1) : pick);
    }
  };

  const onPathKey = (ev: KeyboardEvent) => {
    if (completeOpen() && completeMatches().length > 0) {
      const matches = completeMatches();
      if (ev.key === 'ArrowDown') {
        ev.preventDefault();
        setCompleteIdx((completeIdx() + 1) % matches.length);
        return;
      }
      if (ev.key === 'ArrowUp') {
        ev.preventDefault();
        setCompleteIdx((completeIdx() - 1 + matches.length) % matches.length);
        return;
      }
      if (ev.key === 'Tab') {
        ev.preventDefault();
        pickCompletion(completeIdx(), false);
        return;
      }
      if (ev.key === 'Escape') {
        ev.preventDefault();
        closeCompleteDropdown();
        return;
      }
      if (ev.key === 'Enter') {
        ev.preventDefault();
        pickCompletion(completeIdx(), true);
        return;
      }
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      navigateTo(pathInputValue());
    } else if (ev.key === 'Escape') {
      closeCompleteDropdown();
    }
  };

  // ---- drag-and-drop ----

  const onDragStart = (ev: DragEvent, p: string) => {
    if (!ev.dataTransfer) return;
    ev.dataTransfer.effectAllowed = 'copy';
    ev.dataTransfer.setData('application/x-wash-path', p);
    ev.dataTransfer.setData('text/plain', p);
  };

  const onDrop = (ev: DragEvent) => {
    if (!ev.dataTransfer) return;
    const p = ev.dataTransfer.getData('application/x-wash-path');
    if (!p) return;
    ev.preventDefault();
    setStatusOverride(`Dropped: ${p}`);
    setStatusDropPath(p);
  };

  // ---- state persistence ----

  const persist = () => {
    if (!props.instance) return;
    const s: PersistedState = {
      path: path() || undefined,
      expanded: Object.keys(expanded),
      sort_key: sortKey(),
      sort_desc: sortDesc(),
      show_hidden: showHidden(),
      info_open: infoOpen(),
    };
    send({ kind: 'save_state', state: s });
  };

  const restoreFrom = (s: PersistedState) => {
    if (s.sort_key) setSortKey(s.sort_key);
    if (typeof s.sort_desc === 'boolean') setSortDesc(s.sort_desc);
    if (typeof s.show_hidden === 'boolean') setShowHidden(s.show_hidden);
    if (typeof s.info_open === 'boolean') setInfoOpen(s.info_open);
    if (s.expanded) {
      for (const p of s.expanded) setExpanded(p, true);
    }
    if (s.path) selectPath(s.path, false);
    else send({ kind: 'request_initial' });
  };

  // ---- listings sort/filter ----

  const sortedFiltered = (entries: Entry[]): Entry[] => {
    let out = entries;
    if (!showHidden()) out = out.filter((e) => !e.name.startsWith('.'));
    out = out.slice();
    const desc = sortDesc();
    const key = sortKey();
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
  };

  // Flatten the visible tree into a list of {entry, path, depth} rows
  // — Solid's <For> renders the list; toggling expand triggers a fresh
  // computation here automatically.
  const visibleRows = createMemo<Array<{ entry: Entry; path: string; depth: number }>>(() => {
    const rows: Array<{ entry: Entry; path: string; depth: number }> = [];
    const walk = (p: string, depth: number) => {
      const entries = listings[p];
      if (!entries) return;
      for (const e of sortedFiltered(entries)) {
        const childPath = joinPath(p, e.name);
        rows.push({ entry: e, path: childPath, depth });
        if (e.type === 'dir' && expanded[childPath] && listings[childPath]) {
          walk(childPath, depth + 1);
        }
      }
    };
    if (listings['/']) walk('/', 0);
    return rows;
  });

  // visibleCount is the total entries visible right now. Updates
  // automatically as folders expand/collapse — replacing the old "X
  // entries (just the last list)" status that never refreshed.
  const visibleCount = createMemo(() => visibleRows().length);

  // statusBar text — derived. While statusOverride is set (drop /
  // error), show it; otherwise the live visible-entry count.
  const statusBar = createMemo(() => {
    const override = statusOverride();
    if (override) return override;
    if (!rootInitialized()) return 'loading…';
    return `${visibleCount()} entries`;
  });

  // ---- menus ----

  const closeMenu = () => setMenu(null);

  const openSortMenu = (ev: MouseEvent) => {
    const rect = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    const my = props.host.getBoundingClientRect();
    setMenu({ kind: 'sort', left: rect.right - my.left - 160, top: rect.bottom - my.top + 4 });
  };

  const openContextMenu = (ev: MouseEvent, entry: Entry, p: string) => {
    ev.preventDefault();
    setSelectedEntry(entry);
    setPath(p);
    setPathInputValue(p);
    if (entry.type === 'file') sendRead(p);
    const my = props.host.getBoundingClientRect();
    setMenu({ kind: 'context', left: ev.clientX - my.left, top: ev.clientY - my.top, entry, path: p });
  };

  // ---- lifecycle: events ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      if (s) restoreFrom(s);
      else send({ kind: 'request_initial' });
    };
    const onKey = (ev: KeyboardEvent) => {
      if ((ev.ctrlKey || ev.metaKey) && ev.key === 'c' && path() && document.activeElement !== pathInputEl) {
        ev.preventDefault();
        send({ kind: 'clipboard_copy_path', path: path() });
      }
    };
    const onDocMouseDown = (ev: MouseEvent) => {
      if (menu()) {
        // Solid renders menus inside the host; clicks outside close.
        const target = ev.target as Node;
        const menuEl = props.host.querySelector('[data-testid="fm-sort-menu"], [data-testid="fm-context-menu"]');
        if (menuEl && !menuEl.contains(target)) closeMenu();
      }
    };
    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('wash:state', onState);
    props.host.addEventListener('keydown', onKey);
    document.addEventListener('mousedown', onDocMouseDown);
    if (!props.host.hasAttribute('tabindex')) props.host.setAttribute('tabindex', '0');
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
      props.host.removeEventListener('keydown', onKey);
      document.removeEventListener('mousedown', onDocMouseDown);
    });
  });

  // ---- view ----

  return (
    <>
      {/* toolbar */}
      <div style={toolbarStyle}>
        <button type="button" data-testid="fm-home" title="Home" style={iconBtnStyle} onClick={goHome}>
          <HomeIcon size={14} />
        </button>
        <button type="button" data-testid="fm-back" title="Back" style={iconBtnStyle} onClick={goBack}>
          <ArrowLeft size={14} />
        </button>
        <button type="button" data-testid="fm-up" title="Up" style={iconBtnStyle} onClick={goUp}>
          <ArrowUp size={14} />
        </button>
        <input
          ref={pathInputEl!}
          type="text"
          data-testid="fm-path"
          spellcheck={false}
          placeholder="path"
          style={pathInputStyle}
          value={pathInputValue()}
          onInput={(e) => onPathInput(e.currentTarget.value)}
          onKeyDown={onPathKey}
          onBlur={() => setTimeout(closeCompleteDropdown, 100)}
        />
        <button
          type="button"
          data-testid="fm-reload"
          title="Reload"
          style={iconBtnStyle}
          onClick={() => { if (path()) invalidateAndList(parentPath(path())); }}
        >
          <RotateCw size={14} />
        </button>
        <button type="button" data-testid="fm-sort" title="Sort" style={iconBtnStyle} onClick={openSortMenu}>
          <ArrowUpDown size={14} />
        </button>
      </div>

      {/* body: tree + preview/info */}
      <div style={bodyStyle}>
        <div
          data-testid="fm-list"
          style={treeStyle}
          onDragOver={(ev) => {
            if (ev.dataTransfer?.types.includes('application/x-wash-path')) {
              ev.preventDefault();
              ev.dataTransfer.dropEffect = 'copy';
            }
          }}
          onDrop={onDrop}
        >
          <For each={visibleRows()}>
            {(row) => <TreeRow
              entry={row.entry}
              path={row.path}
              depth={row.depth}
              selected={path() === row.path}
              expanded={!!expanded[row.path]}
              onClick={() => {
                const now = Date.now();
                const isDouble = row.path === lastClickPath && now - lastClickTime < 400;
                lastClickPath = row.path;
                lastClickTime = now;
                if (isDouble && row.entry.type === 'symlink') {
                  followSymlink(row.entry, row.path);
                  return;
                }
                selectPath(row.path, true);
              }}
              onToggle={() => toggleExpand(row.path)}
              onContextMenu={(ev) => openContextMenu(ev, row.entry, row.path)}
              onDragStart={(ev) => onDragStart(ev, row.path)}
            />}
          </For>
        </div>
        <div style={{ display: 'grid', 'grid-template-rows': '1fr auto', overflow: 'hidden' }}>
          <PreviewPane content={previewContent()} />
          <InfoSection
            open={infoOpen()}
            onToggle={toggleInfo}
            entry={selectedEntry()}
            path={path()}
          />
        </div>
      </div>

      {/* status */}
      <div
        data-testid="fm-status"
        data-drop-path={statusDropPath() || undefined}
        style={statusStyle}
      >
        {statusBar()}
      </div>

      {/* overlays */}
      <Show when={completeOpen() && completeMatches().length > 0}>
        <AutocompleteDropdown
          host={props.host}
          anchor={pathInputEl}
          matches={completeMatches()}
          idx={completeIdx()}
          onHover={(i) => setCompleteIdx(i)}
          onPick={(i) => pickCompletion(i, true)}
        />
      </Show>

      <Show when={menu()?.kind === 'sort'}>
        <SortMenu
          left={(menu() as { left: number }).left}
          top={(menu() as { top: number }).top}
          sortKey={sortKey()}
          sortDesc={sortDesc()}
          showHidden={showHidden()}
          onPick={(k) => {
            if (sortKey() === k) setSortDesc(!sortDesc());
            else { setSortKey(k); setSortDesc(false); }
            closeMenu();
            persist();
          }}
          onToggleHidden={() => {
            setShowHidden(!showHidden());
            closeMenu();
            persist();
          }}
        />
      </Show>

      <Show when={menu()?.kind === 'context'}>
        {(() => {
          const m = menu() as { kind: 'context'; left: number; top: number; entry: Entry; path: string };
          return (
            <ContextMenu
              left={m.left}
              top={m.top}
              entry={m.entry}
              path={m.path}
              onOpen={() => {
                closeMenu();
                if (m.entry.type === 'symlink') followSymlink(m.entry, m.path);
                else selectPath(m.path, true);
              }}
              onCopy={() => {
                closeMenu();
                send({ kind: 'clipboard_copy_path', path: m.path });
              }}
              onInfo={() => {
                closeMenu();
                if (!infoOpen()) toggleInfo();
              }}
            />
          );
        })()}
      </Show>
    </>
  );
};

// ---- sub-components ----

const TreeRow: Component<{
  entry: Entry;
  path: string;
  depth: number;
  selected: boolean;
  expanded: boolean;
  onClick: () => void;
  onToggle: () => void;
  onContextMenu: (ev: MouseEvent) => void;
  onDragStart: (ev: DragEvent) => void;
}> = (props) => {
  const [hover, setHover] = createSignal(false);
  return (
    <div
      data-testid={`fm-entry-${props.entry.name}`}
      data-type={props.entry.type}
      data-path={props.path}
      draggable="true"
      onDragStart={props.onDragStart}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={props.onClick}
      onContextMenu={props.onContextMenu}
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '4px',
        padding: `3px 8px 3px ${8 + props.depth * 16}px`,
        background: props.selected ? '#23234a' : hover() ? '#1d1d30' : 'transparent',
        color: '#eee',
        cursor: 'pointer',
        'user-select': 'none',
        font: '13px ui-sans-serif,system-ui,sans-serif',
      }}
    >
      <span
        style={{ width: '12px', display: 'inline-flex', 'align-items': 'center', 'justify-content': 'center', opacity: 0.6, cursor: 'pointer' }}
        onClick={(ev) => {
          if (props.entry.type === 'dir') {
            ev.stopPropagation();
            props.onToggle();
          }
        }}
      >
        <Show when={props.entry.type === 'dir'}>
          {props.expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        </Show>
      </span>
      <span style={{ width: '14px', display: 'inline-flex', 'align-items': 'center', 'justify-content': 'center', opacity: 0.8 }}>
        <EntryIcon entry={props.entry} />
      </span>
      <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
        {props.entry.name}
      </span>
      <span style={{ opacity: 0.5, font: '11px ui-monospace,Menlo,Consolas,monospace' }}>
        {props.entry.type === 'file' ? humanSize(props.entry.size) : ''}
      </span>
    </div>
  );
};

type PreviewContent = { binary: boolean; size: number; text: string; truncated: boolean };

const PreviewPane: Component<{ content: PreviewContent | null }> = (props) => {
  return (
    <div data-testid="fm-preview" style={previewStyle}>
      <Show when={props.content} fallback="(select a file to preview)">
        {(c) => (
          <>
            {c().binary
              ? `(binary, ${humanSize(c().size)})`
              : c().text + (c().truncated ? `\n\n--- truncated (${humanSize(c().size)} total) ---` : '')}
          </>
        )}
      </Show>
    </div>
  );
};

const InfoSection: Component<{
  open: boolean;
  onToggle: () => void;
  entry: Entry | null;
  path: string;
}> = (props) => {
  return (
    <div style={{ 'border-top': '1px solid #2a2a3a', background: '#15152a' }}>
      <button
        type="button"
        data-testid="fm-info-toggle"
        style={{ ...infoToggleStyle, display: 'flex', 'align-items': 'center', gap: '6px' }}
        onClick={props.onToggle}
      >
        {props.open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <span>Info</span>
      </button>
      <Show when={props.open}>
        <div data-testid="fm-info" style={infoBodyStyle}>
          <Show when={props.entry} fallback="(no selection)">
            {(e) => {
              const entry = e();
              const rows: Array<[string, string]> = [
                ['Path', props.path],
                ['Type', entry.type],
                ['Size', humanSize(entry.size)],
                ['Modified', new Date(entry.mod_unix * 1000).toLocaleString()],
                ['Permissions', entry.perm + ` (${octalPerm(entry.mode)})`],
              ];
              if (entry.type === 'symlink') {
                rows.push(['Link target', entry.link_to ?? `(${entry.link_err ?? 'unresolved'})`]);
              }
              return <For each={rows}>
                {([k, v]) => (
                  <div style={{ display: 'flex', gap: '10px', padding: '2px 0' }}>
                    <span style={{ width: '110px', opacity: 0.6, 'flex-shrink': 0 }}>{k}</span>
                    <span style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{v}</span>
                  </div>
                )}
              </For>;
            }}
          </Show>
        </div>
      </Show>
    </div>
  );
};

const SortMenu: Component<{
  left: number;
  top: number;
  sortKey: SortKey;
  sortDesc: boolean;
  showHidden: boolean;
  onPick: (k: SortKey) => void;
  onToggleHidden: () => void;
}> = (props) => {
  const arrow = (k: SortKey): JSX.Element => {
    if (props.sortKey !== k) return null;
    return props.sortDesc ? <ChevronDown size={12} /> : <ChevronUp size={12} />;
  };
  return (
    <div data-testid="fm-sort-menu" style={{ ...menuBoxStyle, left: `${props.left}px`, top: `${props.top}px` }}>
      <MenuItem testid="fm-sort-name" label="Name" trailing={arrow('name')} onClick={() => props.onPick('name')} />
      <MenuItem testid="fm-sort-mtime" label="Modified" trailing={arrow('mtime')} onClick={() => props.onPick('mtime')} />
      <MenuItem testid="fm-sort-size" label="Size" trailing={arrow('size')} onClick={() => props.onPick('size')} />
      <MenuItem testid="fm-sort-type" label="Type" trailing={arrow('type')} onClick={() => props.onPick('type')} />
      <div style={{ height: '1px', background: '#2a2a3a', margin: '4px 0' }} />
      <MenuItem
        testid="fm-show-hidden"
        label="Show hidden"
        trailing={props.showHidden ? <Check size={12} /> : <Square size={12} />}
        onClick={props.onToggleHidden}
      />
    </div>
  );
};

const ContextMenu: Component<{
  left: number;
  top: number;
  entry: Entry;
  path: string;
  onOpen: () => void;
  onCopy: () => void;
  onInfo: () => void;
}> = (props) => {
  return (
    <div data-testid="fm-context-menu" style={{ ...menuBoxStyle, left: `${props.left}px`, top: `${props.top}px` }}>
      <MenuItem testid="fm-ctx-open" label="Open" onClick={props.onOpen} />
      <MenuItem testid="fm-ctx-copy" label="Copy path" onClick={props.onCopy} />
      <MenuItem testid="fm-ctx-info" label="Show info" onClick={props.onInfo} />
    </div>
  );
};

const MenuItem: Component<{
  testid?: string;
  label: string;
  trailing?: JSX.Element;
  onClick: () => void;
}> = (props) => {
  const [hover, setHover] = createSignal(false);
  return (
    <button
      type="button"
      data-testid={props.testid}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onClick={props.onClick}
      style={{
        display: 'flex',
        'align-items': 'center',
        width: '100%',
        'text-align': 'left',
        background: hover() ? '#23233a' : 'transparent',
        color: '#eee',
        border: 'none',
        padding: '6px 14px',
        cursor: 'pointer',
        font: '13px ui-sans-serif,system-ui,sans-serif',
      }}
    >
      <span style={{ flex: 1 }}>{props.label}</span>
      <Show when={props.trailing}>{props.trailing}</Show>
    </button>
  );
};

const AutocompleteDropdown: Component<{
  host: HTMLElement;
  anchor: HTMLInputElement;
  matches: string[];
  idx: number;
  onHover: (i: number) => void;
  onPick: (i: number) => void;
}> = (props) => {
  const [pos, setPos] = createSignal({ left: 0, top: 0, width: 240 });
  onMount(() => {
    const rect = props.anchor.getBoundingClientRect();
    const my = props.host.getBoundingClientRect();
    setPos({ left: rect.left - my.left, top: rect.bottom - my.top + 2, width: rect.width });
  });
  return (
    <div
      data-testid="fm-complete"
      style={{
        position: 'absolute',
        background: '#15152a',
        border: '1px solid #2a2a3a',
        'border-radius': '4px',
        padding: '2px 0',
        'min-width': '240px',
        'max-height': '280px',
        'overflow-y': 'auto',
        'box-shadow': '0 8px 20px rgba(0,0,0,0.5)',
        'z-index': 1500,
        font: '12px ui-monospace,Menlo,Consolas,monospace',
        left: `${pos().left}px`,
        top: `${pos().top}px`,
        width: `${pos().width}px`,
      }}
    >
      <For each={props.matches}>
        {(match, i) => (
          <div
            data-testid={`fm-complete-${i()}`}
            style={{
              padding: '4px 8px',
              cursor: 'pointer',
              color: '#eee',
              background: i() === props.idx ? '#23234a' : 'transparent',
              'white-space': 'nowrap',
              overflow: 'hidden',
              'text-overflow': 'ellipsis',
            }}
            onMouseEnter={() => props.onHover(i())}
            onMouseDown={(ev) => {
              ev.preventDefault();
              props.onPick(i());
            }}
          >
            {match}
          </div>
        )}
      </For>
    </div>
  );
};

// ---- styles ----

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '4px',
  padding: '0 8px',
  background: '#181828',
};

const iconBtnStyle: JSX.CSSProperties = {
  background: 'transparent',
  color: '#eee',
  border: '1px solid #2a2a3a',
  'border-radius': '3px',
  padding: '4px 8px',
  cursor: 'pointer',
  font: '13px ui-sans-serif,system-ui,sans-serif',
  'min-width': '30px',
};

const pathInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: '#10101a',
  color: '#eee',
  border: '1px solid #2a2a3a',
  'border-radius': '3px',
  padding: '4px 8px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
  outline: 'none',
};

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '1fr 1fr',
  overflow: 'hidden',
  'border-top': '1px solid #2a2a3a',
  'border-bottom': '1px solid #2a2a3a',
};

const treeStyle: JSX.CSSProperties = {
  overflow: 'auto',
  background: '#181828',
  padding: '4px 0',
  'border-right': '1px solid #2a2a3a',
};

const previewStyle: JSX.CSSProperties = {
  overflow: 'auto',
  padding: '10px 12px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
  'white-space': 'pre',
  background: '#0c0c14',
};

const infoToggleStyle: JSX.CSSProperties = {
  display: 'block',
  width: '100%',
  'text-align': 'left',
  background: 'transparent',
  color: '#eee',
  border: 'none',
  padding: '6px 12px',
  cursor: 'pointer',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
};

const infoBodyStyle: JSX.CSSProperties = {
  padding: '8px 12px 12px',
  font: '12px ui-monospace,Menlo,Consolas,monospace',
  'border-top': '1px solid #2a2a3a',
};

const menuBoxStyle: JSX.CSSProperties = {
  position: 'absolute',
  background: '#15152a',
  border: '1px solid #2a2a3a',
  'border-radius': '4px',
  padding: '4px 0',
  'min-width': '160px',
  'box-shadow': '0 6px 16px rgba(0,0,0,0.5)',
  'z-index': 1000,
};

const statusStyle: JSX.CSSProperties = {
  padding: '0 10px',
  font: '11px ui-monospace,Menlo,Consolas,monospace',
  opacity: 0.6,
  display: 'flex',
  'align-items': 'center',
};

// ---- helpers ----

const EntryIcon: Component<{ entry: Entry }> = (props) => {
  switch (props.entry.type) {
    case 'dir':
      return <FolderIcon size={12} />;
    case 'symlink':
      return <Link2 size={12} />;
    case 'file':
      return <FileIcon size={12} />;
    default:
      return <FileIcon size={12} />;
  }
};

function joinPath(parent: string, name: string): string {
  if (parent.endsWith('/')) return parent + name;
  return parent + '/' + name;
}

function parentPath(p: string): string {
  if (!p || p === '/') return '/';
  const i = p.lastIndexOf('/');
  if (i <= 0) return '/';
  return p.slice(0, i);
}

function baseName(p: string): string {
  const i = p.lastIndexOf('/');
  return i < 0 ? p : p.slice(i + 1);
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

// ---- custom element wrapper ----

class WashAppFM extends HTMLElement {
  private cleanup?: () => void;

  connectedCallback() {
    this.style.cssText = [
      'display:grid',
      'grid-template-rows:36px 1fr 22px',
      'width:100%',
      'height:100%',
      'background:#10101a',
      'color:#eee',
      'font:13px ui-sans-serif,system-ui,sans-serif',
      'box-sizing:border-box',
      'position:relative',
    ].join(';');

    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.cleanup = render(() => <App instance={instance} host={this} />, this);
  }

  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}

if (!customElements.get('wash-app-fm')) {
  customElements.define('wash-app-fm', WashAppFM);
}

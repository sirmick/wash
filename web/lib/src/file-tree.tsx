// FileTree — the shared tree/list view behind wash-fm's main pane and
// wash-edit's sidebar. The pure tree WALK (flattenTree) already lives in
// @wash/fs-client; this is the Solid RENDER half: the scroll container, the
// optional sortable column header, per-row chevron/icon/name/rename, drag-drop
// plumbing, row-identity stabilisation, and scroll-into-view-on-navigate.
//
// It is deliberately "dumb": the caller owns all state (listings, expanded,
// selection, sort) and passes the already-flattened `rows`; FileTree renders
// them and emits events. App-specific looks (icons, name/size/date cells,
// colour tints, badges) come in as render hooks + a column config so the two
// consumers — fm (rich: 1-4 responsive columns, multi-select, tint, setid
// badge) and edit (minimal: name only, single-select) — share one body.
//
// Reactivity note: the caller passes `rows` as a plain reactive expression
// (Solid wraps prop reads in getters), so a fresh flattenTree() result flows
// through automatically. Identity stabilisation below keeps <For> from tearing
// down unchanged rows (see the prevRows comment).

import type { Component, JSX } from 'solid-js';
import { createMemo, createSignal, createEffect, For, Show } from 'solid-js';
import { ChevronRight, ChevronDown, ChevronUp } from 'lucide-solid';
import { tokens } from './tokens';

// A single flattened row, matching @wash/fs-client's flattenTree output.
export interface FileTreeRow<E> {
  entry: E;
  path: string;
  depth: number;
  childCount?: number;
}

// The minimal entry shape FileTree needs to render a row. Consumers pass their
// own richer Entry; only name + type are read here (everything else is reached
// through the render hooks, which receive the full entry).
export interface FileTreeEntry {
  name: string;
  type: string;
}

// An extra column beyond the implicit Name column (which always holds the
// chevron + icon + name). fm supplies Size/Modified/Created; edit supplies
// none. `track` is the CSS grid track size; `cell` renders the value.
export interface FileTreeColumn<E> {
  key: string;
  header: string;
  track: string;
  align?: 'left' | 'right';
  sortable?: boolean;
  cell: (entry: E, ctx: { childCount?: number; renaming: boolean }) => JSX.Element | string;
}

export interface FileTreeProps<E extends FileTreeEntry> {
  // Already-flattened, sorted rows (caller runs flattenTree in a memo).
  rows: FileTreeRow<E>[];

  // ---- per-row state predicates ----
  isSelected: (path: string) => boolean;
  isExpanded: (path: string) => boolean;
  isCurrent?: (path: string) => boolean; // bold name (fm "current dir"); omit → never bold
  isDropTarget?: (path: string) => boolean;

  // ---- render hooks (app-specific looks) ----
  renderIcon: (entry: E, path: string) => JSX.Element;
  rowTint?: (entry: E) => string | undefined; // name + icon colour; undefined → default fg
  rowHint?: (entry: E) => string | undefined; // value for the data-hint attribute (e2e + styling)
  rowTrailing?: (entry: E) => JSX.Element | null; // slot after the name (fm's setid badge)

  // ---- columns + header ----
  // Extra columns beyond Name. Omit/[] → name-only (edit). The Name track is
  // always '1fr'; extra tracks are appended in order.
  columns?: FileTreeColumn<E>[];
  // When present, a sticky sortable header is rendered. Omit → no header (edit).
  header?: { sortKey: string; sortDesc: boolean; onSort: (key: string) => void; nameKey?: string };

  // ---- events ----
  onRowClick: (path: string, entry: E, ev: MouseEvent) => void;
  onRowDblClick?: (path: string, entry: E) => void;
  onToggle: (path: string, entry: E) => void;
  onRowContextMenu?: (ev: MouseEvent, entry: E, path: string) => void;
  onRowDragStart?: (ev: DragEvent, path: string, entry: E) => void;
  onRowDragEnd?: () => void;
  onRowDragOver?: (ev: DragEvent, path: string, entry: E) => void;
  onRowDrop?: (ev: DragEvent, path: string, entry: E) => void;

  // ---- inline rename ----
  renaming?: (path: string) => { draft: string } | null;
  onRenameInput?: (val: string) => void;
  onRenameCommit?: () => void;
  onRenameCancel?: () => void;

  // ---- container ----
  // data-testid prefix for rows/chevrons/rename input/header (e.g. 'fm', 'edit').
  testIdPrefix: string;
  // data-testid for the scroll container itself (fm's 'fm-list'); omit for none.
  listTestId?: string;
  hoverHighlight?: boolean; // default true
  containerStyle?: JSX.CSSProperties; // merged over the base scroll style
  containerAttrs?: Record<string, string | undefined>; // extra attrs on the container (e.g. data-upload-active)
  onContainerDragOver?: (ev: DragEvent) => void;
  onContainerDragLeave?: (ev: DragEvent) => void;
  onContainerDrop?: (ev: DragEvent) => void;
  onBackgroundClick?: (ev: MouseEvent) => void;
  // Rendered after the header and before the rows (fm's pending-new row +
  // empty-folder placeholder, composed by the caller behind its own <Show>).
  prepend?: JSX.Element;
  // Path to keep scrolled into view; re-runs only when this value changes (not
  // on expand/collapse), so toggling a folder doesn't yank the viewport.
  scrollTarget?: () => string;
}

// Base scroll-container style. overflow-anchor:none pins scrollTop on
// expand/collapse so a toggled folder stays put and its children push content
// down, instead of the browser re-anchoring to a lower row.
const baseContainerStyle: JSX.CSSProperties = {
  overflow: 'auto',
  'overflow-anchor': 'none',
};

const inlineInputStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderFocus}`,
  'border-radius': `${tokens.radiusSm}`,
  padding: '2px 6px',
  font: tokens.type.monoMd,
  outline: 'none',
  width: '100%',
  'box-sizing': 'border-box',
};

const extraCellStyle = (align: 'left' | 'right'): JSX.CSSProperties => ({
  opacity: 0.6,
  font: tokens.type.monoSm,
  'text-align': align,
  'white-space': 'nowrap',
  overflow: 'hidden',
});

const HEADER_ROW_H = 22;

export function FileTree<E extends FileTreeEntry>(props: FileTreeProps<E>): JSX.Element {
  let containerEl: HTMLDivElement | undefined;

  const columns = (): FileTreeColumn<E>[] => props.columns ?? [];
  const template = createMemo(() => ['1fr', ...columns().map((c) => c.track)].join(' '));

  // Identity-stabilising layer. flattenTree returns brand-new wrapper + entry
  // objects every recompute and <For> keys by object reference — so without
  // this, any re-list (even a no-op fs.watch refresh producing value-identical
  // entries) tears down and rebuilds every row's DOM, and a click then races
  // the rebuild ("node detached mid-click"). Reuse the prior row object for a
  // path whose serialised content is unchanged so <For> keeps that DOM.
  let prevRows = new Map<string, { row: FileTreeRow<E>; sig: string }>();
  const stableRows = createMemo<FileTreeRow<E>[]>(() => {
    const next = new Map<string, { row: FileTreeRow<E>; sig: string }>();
    const out = props.rows.map((row) => {
      const sig = JSON.stringify(row);
      const prior = prevRows.get(row.path);
      const stable = prior && prior.sig === sig ? prior.row : row;
      next.set(row.path, { row: stable, sig });
      return stable;
    });
    prevRows = next;
    return out;
  });

  // Scroll the target row into view when it changes (navigation), retrying once
  // rows mount. lastScrolledPath gates the retry so expand/collapse — which
  // changes stableRows() but not scrollTarget() — doesn't re-scroll.
  let lastScrolledPath: string | null = null;
  createEffect(() => {
    const cur = props.scrollTarget?.();
    stableRows();
    if (!cur || cur === lastScrolledPath || !containerEl) return;
    queueMicrotask(() => {
      const row = containerEl?.querySelector(`[data-path="${CSS.escape(cur)}"]`);
      if (!row) return; // not mounted yet — a later stableRows() change retries
      (row as HTMLElement).scrollIntoView({ block: 'nearest' });
      lastScrolledPath = cur;
    });
  });

  const headerCell = (key: string, label: string, align: 'left' | 'right', sortable: boolean): JSX.Element => {
    const h = props.header!;
    const arrow = h.sortKey !== key ? null : h.sortDesc ? <ChevronDown size={11} /> : <ChevronUp size={11} />;
    return (
      <button
        type="button"
        data-testid={`${props.testIdPrefix}-header-${key}`}
        disabled={!sortable}
        onClick={() => sortable && h.onSort(key)}
        style={{
          background: 'transparent',
          border: 'none',
          color: tokens.fgMuted,
          font: tokens.type.textSm,
          cursor: sortable ? 'pointer' : 'default',
          padding: '0 8px',
          height: `${HEADER_ROW_H}px`,
          'box-sizing': 'border-box',
          display: 'flex',
          'align-items': 'center',
          gap: '4px',
          'justify-content': align === 'right' ? 'flex-end' : 'flex-start',
        }}
      >
        <span>{label}</span>
        {arrow}
      </button>
    );
  };

  return (
    <div
      ref={containerEl!}
      data-testid={props.listTestId}
      {...(props.containerAttrs ?? {})}
      style={{ ...baseContainerStyle, ...(props.containerStyle ?? {}) }}
      onDragOver={props.onContainerDragOver}
      onDragLeave={props.onContainerDragLeave}
      onDrop={props.onContainerDrop}
      onClick={(ev) => {
        if (ev.target === ev.currentTarget) props.onBackgroundClick?.(ev);
      }}
    >
      <Show when={props.header}>
        <div
          data-testid={`${props.testIdPrefix}-column-header`}
          style={{
            position: 'sticky',
            top: 0,
            background: tokens.bgMenu,
            'border-bottom': `1px solid ${tokens.borderMenu}`,
            display: 'grid',
            'grid-template-columns': template(),
            'z-index': 2,
            'user-select': 'none',
          }}
        >
          {headerCell(props.header!.nameKey ?? 'name', 'Name', 'left', true)}
          <For each={columns()}>
            {(c) => headerCell(c.key, c.header, c.align ?? 'right', c.sortable ?? true)}
          </For>
        </div>
      </Show>
      {props.prepend}
      <For each={stableRows()}>
        {(row) => {
          const [hover, setHover] = createSignal(false);
          const entry = () => row.entry;
          const renameState = () => props.renaming?.(row.path) ?? null;
          const dropTarget = () => !!props.isDropTarget?.(row.path);
          return (
            <div
              data-testid={`${props.testIdPrefix}-entry-${entry().name}`}
              data-type={entry().type}
              data-path={row.path}
              data-hint={props.rowHint?.(entry())}
              data-selected={props.isSelected(row.path) ? 'true' : undefined}
              data-drop-target={dropTarget() ? 'true' : undefined}
              draggable="true"
              onDragStart={(ev) => props.onRowDragStart?.(ev, row.path, entry())}
              onDragEnd={props.onRowDragEnd}
              onDragOver={(ev) => props.onRowDragOver?.(ev, row.path, entry())}
              onDrop={(ev) => props.onRowDrop?.(ev, row.path, entry())}
              onMouseEnter={() => setHover(true)}
              onMouseLeave={() => setHover(false)}
              onClick={(ev) => props.onRowClick(row.path, entry(), ev)}
              onDblClick={() => props.onRowDblClick?.(row.path, entry())}
              onContextMenu={(ev) => props.onRowContextMenu?.(ev, entry(), row.path)}
              style={{
                display: 'grid',
                'grid-template-columns': template(),
                'align-items': 'center',
                padding: '3px 8px',
                background: dropTarget()
                  ? tokens.bgDropTarget
                  : props.isSelected(row.path)
                  ? tokens.bgRowSelected
                  : (props.hoverHighlight ?? true) && hover()
                  ? tokens.bgRowHover
                  : 'transparent',
                color: tokens.fg,
                cursor: 'pointer',
                'user-select': 'none',
                font: tokens.type.textMd,
                'box-shadow': dropTarget() ? `inset 0 0 0 2px ${tokens.borderDropTarget}` : 'none',
                outline: 'none',
              }}
            >
              {/* Name cell: chevron + icon + name (+ trailing). The tint colours
                  both icon (currentColor) and name. */}
              <span
                style={{
                  display: 'flex',
                  'align-items': 'center',
                  gap: '4px',
                  'padding-left': `${row.depth * 12}px`,
                  overflow: 'hidden',
                  color: props.rowTint?.(entry()) ?? tokens.fg,
                }}
              >
                <span
                  data-testid={`${props.testIdPrefix}-chevron-${entry().name}`}
                  style={{
                    width: '12px',
                    display: 'inline-flex',
                    'align-items': 'center',
                    'justify-content': 'center',
                    opacity: 0.6,
                    'flex-shrink': 0,
                    cursor: entry().type === 'dir' ? 'pointer' : 'default',
                  }}
                  onClick={(ev) => {
                    if (entry().type === 'dir') {
                      ev.stopPropagation();
                      props.onToggle(row.path, entry());
                    }
                  }}
                >
                  <Show when={entry().type === 'dir'}>
                    {props.isExpanded(row.path) ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  </Show>
                </span>
                <span
                  style={{
                    width: '14px',
                    display: 'inline-flex',
                    'align-items': 'center',
                    'justify-content': 'center',
                    opacity: 0.8,
                    'flex-shrink': 0,
                  }}
                >
                  {props.renderIcon(entry(), row.path)}
                </span>
                <span
                  style={{
                    flex: 1,
                    overflow: 'hidden',
                    'text-overflow': 'ellipsis',
                    'white-space': 'nowrap',
                    'font-weight': props.isCurrent?.(row.path) ? 'bold' : 'normal',
                  }}
                >
                  <Show when={renameState()} fallback={entry().name}>
                    <input
                      data-testid={`${props.testIdPrefix}-rename-input`}
                      ref={(el) => setTimeout(() => { el.focus(); el.select(); }, 0)}
                      type="text"
                      value={renameState()!.draft}
                      onInput={(e) => props.onRenameInput?.(e.currentTarget.value)}
                      onClick={(e) => e.stopPropagation()}
                      onKeyDown={(e) => {
                        e.stopPropagation();
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          props.onRenameCommit?.();
                        } else if (e.key === 'Escape') {
                          e.preventDefault();
                          props.onRenameCancel?.();
                        }
                      }}
                      onBlur={() => props.onRenameCommit?.()}
                      style={inlineInputStyle}
                    />
                  </Show>
                </span>
                {props.rowTrailing?.(entry())}
              </span>
              {/* Extra columns */}
              <For each={columns()}>
                {(c) => (
                  <span style={extraCellStyle(c.align ?? 'right')}>
                    {renameState() ? '' : c.cell(entry(), { childCount: row.childCount, renaming: !!renameState() })}
                  </span>
                )}
              </For>
            </div>
          );
        }}
      </For>
    </div>
  );
}

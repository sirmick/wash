// MenuPicker is a pop-out menu for choosing one value from a list that may
// be long: an agent's model setting through OpenRouter offers hundreds, and
// listed inline they made the menu that held them unusable.
//
// It opens beside the row that asked for it (the caller passes that row's
// right edge), ticks the current value and scrolls it into view, and when
// the list is long enough to hunt through it has a filter box that has the
// keyboard from the start: type to narrow, Enter takes the first match,
// Escape closes.

import { For, Show, createMemo, createSignal, onMount } from 'solid-js';
import type { Component } from 'solid-js';
import { Menu, MenuItem } from './menu';
import { Input } from './panel-kit';
import { tokens } from './tokens';

export interface PickerOption {
  value: string;
  label: string;
  /** Shown as the row's tooltip. */
  description?: string;
}

export interface MenuPickerProps {
  x: number;
  y: number;
  /** Heads the list: what is being chosen. */
  title?: string;
  options: PickerOption[];
  current?: string;
  onPick: (value: string) => void;
  onDismiss: () => void;
  'data-testid'?: string;
}

/** Lists at least this long get a filter box. */
export const PICKER_FILTER_AT = 12;

/** filterOptions keeps the options whose label or value contains every
 *  word of the query, case-insensitively. */
export function filterOptions(options: PickerOption[], query: string): PickerOption[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return options;
  return options.filter((o) => {
    const hay = `${o.label} ${o.value}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

export const MenuPicker: Component<MenuPickerProps> = (props) => {
  const [query, setQuery] = createSignal('');
  const shown = createMemo(() => filterOptions(props.options, query()));
  const filtering = () => props.options.length >= PICKER_FILTER_AT;
  const prefix = () => props['data-testid'] ?? 'menu-picker';
  let list!: HTMLDivElement;
  let input: HTMLInputElement | undefined;

  // Picked, then dismissed: a caller's onPick may read state its onDismiss
  // clears.
  const pick = (value: string) => {
    props.onPick(value);
    props.onDismiss();
  };

  onMount(() => {
    input?.focus();
    list.querySelector('[data-current="true"]')?.scrollIntoView({ block: 'center' });
  });

  return (
    <Menu
      x={props.x}
      y={props.y}
      onDismiss={props.onDismiss}
      data-testid={props['data-testid']}
      style={{ width: '320px', display: 'flex', 'flex-direction': 'column', 'overflow-y': 'hidden', 'max-height': 'min(70vh, calc(100vh - 8px))' }}
    >
      <Show when={props.title}>
        <div style={{ padding: '2px 10px 4px', font: tokens.type.textSm, color: tokens.fgMuted }}>{props.title}</div>
      </Show>
      <Show when={filtering()}>
        <div style={{ padding: '0 6px 4px' }}>
          <Input
            ref={input}
            placeholder={`Filter ${props.options.length}…`}
            value={query()}
            onInput={(e) => setQuery(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); props.onDismiss(); }
              if (e.key === 'Enter' && shown().length > 0) { e.preventDefault(); pick(shown()[0].value); }
            }}
            data-testid={`${prefix()}-filter`}
            style={{ width: '100%', 'box-sizing': 'border-box' }}
          />
        </div>
      </Show>
      <div ref={list} style={{ 'overflow-y': 'auto', 'min-height': 0 }}>
        <For each={shown()}>
          {(o) => (
            <div data-current={o.value === props.current ? 'true' : undefined}>
              <MenuItem
                label={o.label}
                title={o.description}
                trailing={o.value === props.current ? <span>✓</span> : undefined}
                onClick={() => pick(o.value)}
                data-testid={`${prefix()}-${o.value}`}
              />
            </div>
          )}
        </For>
        <Show when={shown().length === 0}>
          <div style={{ padding: '4px 10px', color: tokens.fgMuted, font: tokens.type.textSm }}>Nothing matches</div>
        </Show>
      </div>
    </Menu>
  );
};

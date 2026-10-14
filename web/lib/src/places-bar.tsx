// PlacesBar is the icon cluster the Agent, Files, Editor and Terminal
// windows show for each other (docs/PLACES.md). One icon per OTHER app:
//
//   filled  — that app's window is in this window's group; a click brings it
//             forward, wherever it is on the desktop;
//   outline — not yet; a click opens one here and adds it to the group.
//
// The bar's background carries the GROUP's tint, so the windows that belong
// together are recognisable across the desktop at a glance. The tint is
// derived from the group id (groupTint), never assigned: every member
// computes the same colour from the group it already holds, so no message
// has to carry it and no two members can disagree.
//
// Bound-ness is carried by FILL, not colour — the colour is spent on the
// group, and colour alone fails for colourblind users anyway
// (docs/INTERACTION.md).

import { For, Show, createSignal, onCleanup, onMount, type Component, type JSX } from 'solid-js';
import { Bot, FilePen, Folder, Terminal } from 'lucide-solid';
import { tokens } from './tokens';
import { Button } from './button';

export const PLACES_AGENT = 'com.wash.ai';
export const PLACES_FILES = 'com.wash.fm';
export const PLACES_EDITOR = 'com.wash.edit';
export const PLACES_TERMINAL = 'com.wash.term';

/** What the BE pushes (kind "places"): this window's group and its peers. */
export interface PlacesView {
  group: string;
  /** app id → instance id of the group's window for that app. */
  members: Record<string, string>;
}

interface PlaceApp {
  id: string;
  name: string;
  icon: Component<{ size?: number; fill?: string; 'fill-opacity'?: number; 'stroke-width'?: number }>;
}

// Order is the bar's order. Kept identical in every app so an icon is always
// in the same place — muscle memory is the point of a fixed cluster.
const APPS: PlaceApp[] = [
  { id: PLACES_AGENT, name: 'Agent', icon: Bot },
  { id: PLACES_FILES, name: 'Files', icon: Folder },
  { id: PLACES_EDITOR, name: 'Editor', icon: FilePen },
  { id: PLACES_TERMINAL, name: 'Terminal', icon: Terminal },
];

// Fully round ends whatever the height — the bar is a pill, not a card.
const PILL = '999px';

// The gap round the pill on every side when it sits in a menu bar.
const MENUBAR_INSET = '2px';

// The group palette. Deliberately NOT the four apps' own accents (Files blue,
// Terminal green, Editor amber, Agent violet) or anything that reads as one
// of them — a tint the colour of an app would look like a statement about
// which app it is. Red is out too: on a bar it reads as an error. Every entry
// is a theme token, so each pack supplies its own shade.
const PALETTE = [
  tokens.accentCyan,
  tokens.accentMagenta,
  tokens.accentOrange,
  tokens.accentIndigo,
  tokens.accentPink,
  tokens.accentLime,
];

/** Index into the group palette for a group id: FNV-1a, so it is stable
 *  across windows, processes and reloads, and spreads short random ids
 *  evenly. -1 for no group. Exported for tests. */
export function groupTintIndex(group: string): number {
  if (!group) return -1;
  let h = 0x811c9dc5;
  for (let i = 0; i < group.length; i++) {
    h ^= group.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h % PALETTE.length;
}

/** The bar background for a group — a low-alpha wash over the window, so it
 *  stays legible behind the bar's own content in both themes. undefined for
 *  a window in no group, which shows no tint at all. */
export function groupTint(group: string): string | undefined {
  const i = groupTintIndex(group);
  if (i < 0) return undefined;
  return `color-mix(in srgb, ${PALETTE[i]} 16%, transparent)`;
}

/** The accent a group's filled icons are drawn in — the same hue as the
 *  bar, stronger, so the icons and the bar read as one thing. */
function groupInk(group: string): string {
  const i = groupTintIndex(group);
  return i < 0 ? tokens.fg : PALETTE[i];
}

// The title of the window at the other end of a binding, from the shell's
// own window list. Advisory: a tooltip, never a decision.
function titleOf(instanceID: string): string {
  const wash = (window as unknown as { wash?: { windows?: () => Array<{ instanceID: string; title: string }> } }).wash;
  return wash?.windows?.().find((w) => w.instanceID === instanceID)?.title ?? '';
}

export const PlacesBar: Component<{
  /** This window's own app id — its icon is not drawn. */
  self: string;
  view: PlacesView;
  onOpen: (target: string) => void;
  /** Disables every icon (e.g. the window has no folder yet), with a reason. */
  disabled?: string;
  /** Extra tooltip for an UNBOUND icon, e.g. which catalog a new agent will
   *  start on — the "nothing is a surprise at start" line. */
  describe?: (target: string) => string | undefined;
  /** Sits in a window's menu bar (term, edit, ai) rather than a toolbar
   *  with padding of its own (fm): an equal inset all round, so the pill
   *  does not run into the title bar above and the bar's rule below. */
  menuBar?: boolean;
  style?: JSX.CSSProperties;
}> = (props) => {
  // Titles change (a terminal's cwd, an editor's file); re-read them when the
  // shell says the window list moved, so the tooltip names the far end as it
  // is now.
  const [tick, setTick] = createSignal(0);
  onMount(() => {
    const wash = (window as unknown as { wash?: { onWindowsChanged?: (cb: () => void) => () => void } }).wash;
    const off = wash?.onWindowsChanged?.(() => setTick((n) => n + 1));
    if (off) onCleanup(off);
  });

  const others = () => APPS.filter((a) => a.id !== props.self);
  const bound = (id: string) => !!props.view.members[id];
  const title = (a: PlaceApp): string => {
    if (props.disabled) return props.disabled;
    const inst = props.view.members[a.id];
    if (inst) {
      tick();
      const t = titleOf(inst);
      return t ? `Show ${a.name} · ${t}` : `Show ${a.name}`;
    }
    const extra = props.describe?.(a.id);
    return extra ? `Open ${a.name} here · ${extra}` : `Open ${a.name} here`;
  };

  // One segmented pill, not three buttons: the wrapper draws the single
  // border and the pill ends, the buttons sit flush with hairline dividers
  // between them. Each button's radius follows its position so the hover
  // tint (border-radius: inherit in hit.ts) hugs the pill's ends and no
  // overflow clipping is needed — clipping would eat the focus ring.
  const n = () => others().length;
  const segmentRadius = (i: number): string => {
    const r = PILL;
    if (n() === 1) return r;
    if (i === 0) return `${r} 0 0 ${r}`;
    if (i === n() - 1) return `0 ${r} ${r} 0`;
    return '0';
  };

  return (
    <div
      data-testid="places-bar"
      data-group={props.view.group || undefined}
      role="toolbar"
      aria-label="Related windows"
      style={{
        display: 'inline-flex',
        'align-items': 'stretch',
        height: '22px',
        border: `1px solid ${tokens.borderMenu}`,
        'border-radius': PILL,
        background: groupTint(props.view.group),
        ...(props.menuBar ? { margin: `${MENUBAR_INSET} ${MENUBAR_INSET} ${MENUBAR_INSET} auto` } : { 'margin-right': '4px' }),
        ...props.style,
      }}
    >
      <For each={others()}>
        {(a, i) => {
          const Icon = a.icon;
          return (
            <Button
              variant="ghost"
              data-testid={`places-${a.id.slice('com.wash.'.length)}`}
              data-bound={bound(a.id) ? 'true' : 'false'}
              aria-pressed={bound(a.id)}
              aria-label={title(a)}
              title={title(a)}
              disabled={!!props.disabled}
              onClick={() => props.onOpen(a.id)}
              style={{
                display: 'inline-flex',
                'align-items': 'center',
                'justify-content': 'center',
                padding: '0 8px',
                border: 'none',
                'border-left': i() === 0 ? 'none' : `1px solid ${tokens.borderMenu}`,
                'border-radius': segmentRadius(i()),
                color: bound(a.id) ? groupInk(props.view.group) : tokens.fgMuted,
              }}
            >
              <Show when={bound(a.id)} fallback={<Icon size={14} />}>
                {/* Filled: the shape change that says "bound" without relying
                    on colour. Translucent, because these glyphs are strokes
                    drawn in the SAME colour — an opaque fill would swallow the
                    terminal's prompt, the bot's eyes and the pen whole. */}
                <Icon size={14} fill="currentColor" fill-opacity={0.3} stroke-width={2.25} />
              </Show>
            </Button>
          );
        }}
      </For>
    </div>
  );
};

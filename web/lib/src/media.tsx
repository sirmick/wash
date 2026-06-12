// Media kit — the shared player chrome used by the sidebar AudioWidget
// and (planned) the native Music/Radio/video windows. Pure renderers +
// callbacks; all state lives in com.wash.audio (docs/AUDIO.md). Grouped
// in one file like panel-kit.tsx.

import type { Component, JSX } from 'solid-js';
import { For, Show } from 'solid-js';
import { Pause, Play, SkipBack, SkipForward, Volume2 } from 'lucide-solid';
import { tokens } from './tokens';

const transportBtn: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '4px 10px',
  cursor: 'pointer',
};

export interface TransportControlsProps {
  /** 'playing' shows Pause; anything else shows Play. */
  status: string;
  onPrev: () => void;
  onPlay: () => void;
  onPause: () => void;
  onNext: () => void;
  /** icon px, default 14. */
  size?: number;
}

/** prev / play-pause / next, lucide icons. Testids match the legacy
 *  AudioWidget (audio-prev/play/pause/next) so e2e + the control plane
 *  keep working across the sidebar and the apps. */
export const TransportControls: Component<TransportControlsProps> = (props) => {
  const sz = () => props.size ?? 14;
  return (
    <div style={{ display: 'flex', gap: '6px', 'justify-content': 'center' }}>
      <button
        type="button"
        data-testid="audio-prev"
        style={transportBtn}
        onClick={() => props.onPrev()}
        title="Previous"
        aria-label="Previous"
      >
        <SkipBack size={sz()} />
      </button>
      <Show
        when={props.status === 'playing'}
        fallback={
          <button
            type="button"
            data-testid="audio-play"
            style={transportBtn}
            onClick={() => props.onPlay()}
            title="Play"
            aria-label="Play"
          >
            <Play size={sz()} />
          </button>
        }
      >
        <button
          type="button"
          data-testid="audio-pause"
          style={transportBtn}
          onClick={() => props.onPause()}
          title="Pause"
          aria-label="Pause"
        >
          <Pause size={sz()} />
        </button>
      </Show>
      <button
        type="button"
        data-testid="audio-next"
        style={transportBtn}
        onClick={() => props.onNext()}
        title="Next"
        aria-label="Next"
      >
        <SkipForward size={sz()} />
      </button>
    </div>
  );
};

export interface VolumeSliderProps {
  /** 0..1 */
  value: number;
  onInput: (v: number) => void;
  'data-testid'?: string;
}

/** Volume2 icon + 0..100 range, emitting 0..1. */
export const VolumeSlider: Component<VolumeSliderProps> = (props) => (
  <label
    style={{ display: 'flex', 'align-items': 'center', gap: '6px', 'font-size': tokens.fontSizeSm, opacity: 0.8 }}
  >
    <Volume2 size={13} aria-label="Volume" />
    <input
      data-testid={props['data-testid'] ?? 'audio-volume'}
      type="range"
      min="0"
      max="100"
      value={Math.round(props.value * 100)}
      style={{ flex: 1, 'accent-color': tokens.accentGreen }}
      onInput={(e) => props.onInput(Number(e.currentTarget.value) / 100)}
    />
  </label>
);

export interface NowPlayingProps {
  title: string;
  /** secondary line (artist / station / app). */
  subtitle?: string;
  /** small right-aligned meta on the title row (e.g. "album · 3:42", "128k aac"). */
  meta?: string;
  'data-testid'?: string;
  'data-status'?: string;
}

/** Compact now-playing header: bold title (+ optional right-aligned meta)
 *  over a muted subtitle. */
export const NowPlaying: Component<NowPlayingProps> = (props) => (
  <div data-testid={props['data-testid']} data-status={props['data-status']}>
    <div style={{ display: 'flex', 'align-items': 'baseline', gap: '8px' }}>
      <div
        style={{
          flex: 1,
          'font-weight': 600,
          color: tokens.fg,
          overflow: 'hidden',
          'text-overflow': 'ellipsis',
          'white-space': 'nowrap',
          'font-size': tokens.fontSizeMd,
        }}
      >
        {props.title || 'Unknown'}
      </div>
      <Show when={props.meta}>
        <div style={{ color: tokens.fgMuted, 'font-size': tokens.fontSizeSm, 'white-space': 'nowrap', 'flex-shrink': 0 }}>
          {props.meta}
        </div>
      </Show>
    </div>
    <Show when={props.subtitle}>
      <div style={{ opacity: 0.7, 'font-size': tokens.fontSizeSm }}>{props.subtitle}</div>
    </Show>
  </div>
);

function fmtTime(sec: number): string {
  if (!sec || sec < 0 || !isFinite(sec)) return '0:00';
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${s.toString().padStart(2, '0')}`;
}

export interface SeekBarProps {
  pos: number;
  dur: number;
  /** seekable (Music) shows a draggable bar + total; non-seekable (Radio)
   *  shows "● LIVE" and elapsed only. Default true. */
  seekable?: boolean;
  onSeek?: (sec: number) => void;
}

/** Elapsed · progress · total. Click/drag to seek when seekable. */
export const SeekBar: Component<SeekBarProps> = (props) => {
  const seekable = () => props.seekable !== false;
  return (
    <div
      style={{
        display: 'flex',
        'align-items': 'center',
        gap: '8px',
        'font-size': tokens.fontSizeSm,
        color: tokens.fgMuted,
        'font-variant': 'tabular-nums',
      }}
    >
      <span>{fmtTime(props.pos)}</span>
      <Show
        when={seekable()}
        fallback={
          <div style={{ flex: 1, height: '4px', background: tokens.bgNeutral, 'border-radius': '2px' }} />
        }
      >
        <input
          data-testid="seek"
          type="range"
          min="0"
          max={Math.max(1, Math.floor(props.dur))}
          value={Math.floor(props.pos)}
          style={{ flex: 1, 'accent-color': tokens.accentGreen }}
          onInput={(e) => props.onSeek?.(Number(e.currentTarget.value))}
        />
      </Show>
      <span>{seekable() ? fmtTime(props.dur) : '● LIVE'}</span>
    </div>
  );
};

export interface MediaListProps<T> {
  items: readonly T[];
  /** selected (focused) row index, -1 for none. */
  selected: number;
  /** currently-playing row index, -1/undefined for none. */
  playing?: number;
  onSelect: (i: number) => void;
  /** Enter / double-click on a row. */
  onActivate: (i: number) => void;
  /** render a row's content (left grows, right shrinks). */
  row: (item: T, i: number, playing: boolean) => JSX.Element;
  empty?: JSX.Element;
  'data-testid'?: string;
}

/** A scrollable, keyboard-navigable (↑/↓/Enter) selectable list with a
 *  playing-row accent. Rows are render-prop so each app supplies its own. */
export function MediaList<T>(props: MediaListProps<T>): JSX.Element {
  const onKey = (e: KeyboardEvent) => {
    const n = props.items.length;
    if (!n) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      props.onSelect(Math.min(n - 1, props.selected + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      props.onSelect(Math.max(0, (props.selected < 0 ? 0 : props.selected) - 1));
    } else if (e.key === 'Enter' && props.selected >= 0) {
      e.preventDefault();
      props.onActivate(props.selected);
    }
  };
  return (
    <div
      data-testid={props['data-testid']}
      tabindex={0}
      onKeyDown={onKey}
      style={{
        flex: 1,
        'min-height': 0,
        overflow: 'auto',
        background: tokens.bgNeutral,
        'border-radius': `${tokens.radiusSm}px`,
        outline: 'none',
      }}
    >
      <Show
        when={props.items.length}
        fallback={
          <div style={{ padding: '16px', color: tokens.fgDim, 'font-style': 'italic', 'font-size': tokens.fontSizeSm }}>
            {props.empty ?? 'nothing here'}
          </div>
        }
      >
        <For each={props.items}>
          {(item, i) => {
            const sel = () => props.selected === i();
            const isPlaying = () => props.playing === i();
            return (
              <div
                data-testid={`media-row-${i()}`}
                data-selected={sel() ? 'true' : undefined}
                data-playing={isPlaying() ? 'true' : undefined}
                onClick={() => props.onSelect(i())}
                onDblClick={() => props.onActivate(i())}
                style={{
                  display: 'flex',
                  'align-items': 'center',
                  gap: '8px',
                  padding: '4px 10px',
                  cursor: 'default',
                  'user-select': 'none',
                  'border-left': `2px solid ${isPlaying() ? tokens.accentGreen : 'transparent'}`,
                  background: sel() ? tokens.bgRowSelected : 'transparent',
                  color: isPlaying() ? tokens.accentGreen : tokens.fg,
                  'font-size': tokens.fontSizeBase,
                }}
              >
                {props.row(item, i(), isPlaying())}
              </div>
            );
          }}
        </For>
      </Show>
    </div>
  );
}

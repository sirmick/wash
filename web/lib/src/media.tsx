// Media kit — the shared player chrome used by the sidebar AudioWidget
// and (planned) the native Music/Radio/video windows. Pure renderers +
// callbacks; all state lives in com.wash.audio (docs/AUDIO.md). Grouped
// in one file like panel-kit.tsx.

import type { Component, JSX } from 'solid-js';
import { Show } from 'solid-js';
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
      style={{ flex: 1 }}
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

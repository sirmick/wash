// AudioWidget renders the mixer state fed by com.wash.audio (the audio
// control plane, docs/AUDIO.md §3). The service is the source of truth;
// this widget is a pure renderer + command emitter. It shows the active
// source's now-playing + transport, and the master volume. Playback
// itself lives in each producer's FE — the buttons here send commands
// the service relays to the owning producer.

import type { Component } from 'solid-js';
import { Show } from 'solid-js';

export interface AudioSource {
  id: string;
  app_id: string;
  kind: string;
  title: string;
  artist: string;
  status: string; // playing | paused | stopped
  pos_sec: number;
  dur_sec: number;
}

export interface AudioState {
  sources: AudioSource[];
  master_volume: number;
  master_mute: boolean;
}

export interface AudioWidgetProps {
  state: () => AudioState | null;
  /** Transport for a source by id: play|pause|next|prev|stop. */
  onControl: (id: string, action: string) => void;
  /** Master volume, 0..1. */
  onMasterVolume: (value: number) => void;
}

const btn = {
  background: 'transparent',
  color: '#cfd0d4',
  border: '1px solid #2a2a3a',
  'border-radius': '3px',
  padding: '3px 8px',
  cursor: 'pointer',
  font: '12px ui-sans-serif,system-ui,sans-serif',
} as const;

function fmt(sec: number): string {
  if (!sec || sec < 0) return '0:00';
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${s.toString().padStart(2, '0')}`;
}

export const AudioWidget: Component<AudioWidgetProps> = (props) => {
  const active = () => props.state()?.sources?.[0] ?? null;
  const master = () => props.state()?.master_volume ?? 1;

  return (
    <div
      data-testid="audio-widget"
      style={{ display: 'flex', 'flex-direction': 'column', gap: '8px' }}
    >
      <Show
        when={active()}
        fallback={
          <div
            data-testid="audio-empty"
            style={{
              opacity: 0.5,
              'font-style': 'italic',
              'text-align': 'center',
              padding: '12px 0',
              'font-size': '11px',
            }}
          >
            nothing playing
          </div>
        }
      >
        {(src) => (
          <>
            <div data-testid="audio-nowplaying" data-status={src().status}>
              <div
                style={{
                  'font-weight': 600,
                  color: '#ddd',
                  overflow: 'hidden',
                  'text-overflow': 'ellipsis',
                  'white-space': 'nowrap',
                  'font-size': '12px',
                }}
              >
                {src().title || 'Unknown'}
              </div>
              <div style={{ opacity: 0.7, 'font-size': '11px' }}>
                {src().artist || src().app_id.replace('com.wash.', '')}
              </div>
            </div>

            {/* progress */}
            <div
              style={{
                display: 'flex',
                'align-items': 'center',
                gap: '6px',
                'font-size': '10px',
                opacity: 0.7,
                'font-variant': 'tabular-nums',
              }}
            >
              <span>{fmt(src().pos_sec)}</span>
              <div
                style={{
                  flex: 1,
                  height: '4px',
                  background: '#23252c',
                  'border-radius': '2px',
                  overflow: 'hidden',
                }}
              >
                <div
                  data-testid="audio-progress"
                  style={{
                    height: '100%',
                    width: `${src().dur_sec ? Math.min(100, (src().pos_sec / src().dur_sec) * 100) : 0}%`,
                    background: '#30c060',
                  }}
                />
              </div>
              <span>{fmt(src().dur_sec)}</span>
            </div>

            {/* transport */}
            <div style={{ display: 'flex', gap: '6px', 'justify-content': 'center' }}>
              <button
                type="button"
                data-testid="audio-prev"
                style={btn}
                onClick={() => props.onControl(src().id, 'prev')}
                title="Previous"
              >
                ⏮
              </button>
              <Show
                when={src().status === 'playing'}
                fallback={
                  <button
                    type="button"
                    data-testid="audio-play"
                    style={btn}
                    onClick={() => props.onControl(src().id, 'play')}
                    title="Play"
                  >
                    ▶
                  </button>
                }
              >
                <button
                  type="button"
                  data-testid="audio-pause"
                  style={btn}
                  onClick={() => props.onControl(src().id, 'pause')}
                  title="Pause"
                >
                  ⏸
                </button>
              </Show>
              <button
                type="button"
                data-testid="audio-next"
                style={btn}
                onClick={() => props.onControl(src().id, 'next')}
                title="Next"
              >
                ⏭
              </button>
            </div>
          </>
        )}
      </Show>

      {/* master volume — always shown so the user can set level pre-play */}
      <label
        style={{ display: 'flex', 'align-items': 'center', gap: '6px', 'font-size': '10px', opacity: 0.8 }}
      >
        <span>vol</span>
        <input
          data-testid="audio-volume"
          type="range"
          min="0"
          max="100"
          value={Math.round(master() * 100)}
          style={{ flex: 1 }}
          onInput={(e) => props.onMasterVolume(Number(e.currentTarget.value) / 100)}
        />
      </label>
    </div>
  );
};

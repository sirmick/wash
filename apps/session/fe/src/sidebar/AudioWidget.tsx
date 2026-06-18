// AudioWidget renders the mixer state fed by com.wash.audio (the audio
// control plane, docs/AUDIO.md §3). The service is the source of truth;
// this widget is a pure renderer + command emitter. It shows/drives the
// ACTIVE source (state.active_id, the thing playing or last-played) and
// the master volume; playback lives in each producer's FE. The transport
// cluster + now-playing line come from the shared @wash/ui media kit, so
// the sidebar and the Music/Radio windows stay identical.

import type { Component } from 'solid-js';
import { Show } from 'solid-js';
import { NowPlaying, TransportControls, VolumeSlider, tokens } from '@wash/ui';

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
  /** the source the widget shows/drives; falls back to the front source. */
  active_id?: string;
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

function fmt(sec: number): string {
  if (!sec || sec < 0) return '0:00';
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${s.toString().padStart(2, '0')}`;
}

export const AudioWidget: Component<AudioWidgetProps> = (props) => {
  // The active source = service-chosen active_id, else the front source.
  const active = (): AudioSource | null => {
    const st = props.state();
    if (!st || !st.sources?.length) return null;
    return st.sources.find((s) => s.id === st.active_id) ?? st.sources[0];
  };
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
            <NowPlaying
              data-testid="audio-nowplaying"
              data-status={src().status}
              title={src().title}
              subtitle={src().artist || src().app_id.replace('com.wash.', '')}
            />

            {/* progress (sidebar-specific; apps use a SeekBar) */}
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
                  background: tokens.bgInset,
                  'border-radius': '2px',
                  overflow: 'hidden',
                }}
              >
                <div
                  data-testid="audio-progress"
                  style={{
                    height: '100%',
                    width: `${src().dur_sec ? Math.min(100, (src().pos_sec / src().dur_sec) * 100) : 0}%`,
                    background: tokens.accentGreen,
                  }}
                />
              </div>
              <span>{fmt(src().dur_sec)}</span>
            </div>

            <TransportControls
              status={src().status}
              onPrev={() => props.onControl(src().id, 'prev')}
              onPlay={() => props.onControl(src().id, 'play')}
              onPause={() => props.onControl(src().id, 'pause')}
              onNext={() => props.onControl(src().id, 'next')}
            />
          </>
        )}
      </Show>

      {/* master volume — always shown so the user can set level pre-play */}
      <VolumeSlider value={master()} onInput={(v) => props.onMasterVolume(v)} />
    </div>
  );
};

// wash-music FE — a minimalist NATIVE local music player (docs/MUSIC.md):
// a recursive track list of one selectable folder, basic transport, a
// now-playing info panel, and an in-window volume (model A). Plain wash
// UI built from the @wash/ui media kit + FilePicker; an <audio> element
// does playback (Case-1 fe-decoded over the BE's ingress), and the player
// registers with com.wash.audio (sidebar now-playing + transport).

import {
  Button,
  FilePicker,
  MediaList,
  NowPlaying,
  SeekBar,
  TransportControls,
  VolumeSlider,
  createAudioSource,
  defineWashApp,
  tokens,
  type AudioSource,
  type WashAppProps,
} from '@wash/ui';
import { createSignal, onCleanup, onMount } from 'solid-js';
import { FolderOpen, Volume2 } from 'lucide-solid';

interface Track {
  url: string;
  title: string;
}
interface ScanOk {
  kind: 'scan_ok';
  root: string;
  tracks: Track[];
}

function MusicApp(props: WashAppProps) {
  let audioEl!: HTMLAudioElement;
  let audio: AudioSource | undefined;

  const [tracks, setTracks] = createSignal<Track[]>([]);
  const [root, setRoot] = createSignal('');
  const [index, setIndex] = createSignal(-1); // playing track
  const [selected, setSelected] = createSignal(-1); // keyboard focus
  const [status, setStatus] = createSignal('stopped');
  const [pos, setPos] = createSignal(0);
  const [dur, setDur] = createSignal(0);
  const [srcVol, setSrcVol] = createSignal(1); // in-window volume (model A)
  const [pickerOpen, setPickerOpen] = createSignal(false);
  let masterVol = 1; // from the service's volume cmd; el.volume = master × src

  const current = () => tracks()[index()];
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);
  const applyVolume = () => {
    if (audioEl) audioEl.volume = masterVol * srcVol();
  };

  function loadAndPlay(i: number) {
    const t = tracks()[i];
    if (!t) return;
    setIndex(i);
    setSelected(i);
    audioEl.src = t.url;
    applyVolume();
    void audioEl.play().catch(() => {});
  }
  function play() {
    if (index() < 0) {
      if (tracks().length) loadAndPlay(0);
      return;
    }
    void audioEl.play().catch(() => {});
  }
  const pause = () => audioEl?.pause();
  const next = () => {
    const n = tracks().length;
    if (n) loadAndPlay((index() + 1 + n) % n);
  };
  const prev = () => {
    const n = tracks().length;
    if (n) loadAndPlay((index() - 1 + n) % n);
  };
  const seek = (s: number) => {
    if (audioEl) audioEl.currentTime = s;
  };

  // Transport/volume relayed from the service (sidebar) → drive the player.
  function onCmd(action: string, value?: number) {
    switch (action) {
      case 'play':
        play();
        break;
      case 'pause':
        pause();
        break;
      case 'stop':
        pause();
        if (audioEl) audioEl.currentTime = 0;
        break;
      case 'next':
        next();
        break;
      case 'prev':
        prev();
        break;
      case 'volume':
        if (typeof value === 'number') {
          masterVol = value;
          applyVolume();
        }
        break;
    }
  }

  const folderLabel = () => {
    const r = root();
    if (!r) return 'No folder';
    const parts = r.split('/').filter(Boolean);
    return parts[parts.length - 1] || r;
  };

  onMount(() => {
    audio = createAudioSource({
      instance: props.instance,
      host: props.host,
      snapshot: () => ({ title: current()?.title ?? '', status: status(), pos: pos(), dur: dur() }),
      onCmd,
    });
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as { kind?: string };
      if (m?.kind === 'scan_ok') {
        const s = m as ScanOk;
        setTracks(s.tracks);
        setRoot(s.root);
        setIndex(-1);
        setSelected(s.tracks.length ? 0 : -1);
        audio?.register({ title: s.tracks[0]?.title ?? '' });
      }
    };
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    send({ kind: 'scan', id: 'm-scan' });
  });

  onCleanup(() => audio?.dispose());

  return (
    <div
      style={{
        display: 'flex',
        'flex-direction': 'column',
        height: '100%',
        padding: `${tokens.spaceLg}px`,
        gap: `${tokens.spaceMd}px`,
        'box-sizing': 'border-box',
      }}
    >
      <NowPlaying
        data-testid="music-nowplaying"
        title={current()?.title ?? '—'}
        subtitle={folderLabel()}
        meta={`${tracks().length} ${tracks().length === 1 ? 'track' : 'tracks'}`}
      />

      {/* folder row */}
      <div
        style={{
          display: 'flex',
          'align-items': 'center',
          gap: `${tokens.spaceMd}px`,
          'font-size': tokens.fontSizeSm,
          color: tokens.fgMuted,
        }}
      >
        <FolderOpen size={13} />
        <span
          style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}
          title={root()}
        >
          {root() || 'No folder selected'}
        </span>
        <Button data-testid="pick-folder" onClick={() => setPickerOpen(true)}>
          Change…
        </Button>
      </div>

      <MediaList
        data-testid="track-list"
        items={tracks()}
        selected={selected()}
        playing={index()}
        onSelect={setSelected}
        onActivate={loadAndPlay}
        empty="No audio files in this folder"
        row={(t: Track, _i, playing) => (
          <>
            <span style={{ width: '14px', 'flex-shrink': 0, display: 'inline-flex', 'align-items': 'center' }}>
              {playing ? <Volume2 size={11} /> : null}
            </span>
            <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
              {t.title}
            </span>
          </>
        )}
      />

      <SeekBar pos={pos()} dur={dur()} onSeek={seek} />

      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceLg}px` }}>
        <TransportControls status={status()} onPrev={prev} onPlay={play} onPause={pause} onNext={next} />
        <div style={{ flex: 1 }}>
          <VolumeSlider
            value={srcVol()}
            onInput={(v) => {
              setSrcVol(v);
              applyVolume();
            }}
          />
        </div>
      </div>

      <FilePicker
        open={pickerOpen()}
        mode="directory"
        host={props.host}
        hostInstanceID={props.instance}
        start={root()}
        data-testid="folder-picker"
        onConfirm={(p) => {
          setPickerOpen(false);
          send({ kind: 'scan', id: 'm-scan', root: p });
        }}
        onCancel={() => setPickerOpen(false)}
      />

      <audio
        ref={audioEl}
        style={{ display: 'none' }}
        onPlay={() => {
          setStatus('playing');
          audio?.report();
        }}
        onPause={() => {
          setStatus('paused');
          audio?.report();
        }}
        onTimeUpdate={() => {
          setPos(audioEl.currentTime);
          audio?.report();
        }}
        onLoadedMetadata={() => setDur(audioEl.duration || 0)}
        onEnded={next}
      />
    </div>
  );
}

defineWashApp('wash-app-music', MusicApp, {
  style: `display:block;width:100%;height:100%;background:${tokens.bgWindow};color:${tokens.fg};`,
});

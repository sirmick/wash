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

// Saved FE state (wash:state round-trip). Besides the picked folder,
// playback position survives a reload: track + seek + volume restore
// to a paused-at-position player (autoplay is both blocked by the
// browser and surprising; one click resumes).
interface PersistedState {
  pick?: string;
  track_url?: string;
  track_title?: string;
  pos?: number;
  vol?: number;
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
  // The folder the user picked (the FilePicker path we send to scan); kept
  // so it persists across remount. Empty = default ($WASH_MUSIC_DIR/~/Music).
  let pickedPath = '';

  // Restore bookkeeping: the saved track to re-select once scan_ok
  // delivers the track list, and the position to seek to once the
  // <audio> element has metadata for it.
  let restore: PersistedState | null = null;
  let pendingSeek = -1;
  let lastPersistedPos = 0;
  let persistTimer: ReturnType<typeof setTimeout> | undefined;

  const current = () => tracks()[index()];
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);
  const persist = () => {
    lastPersistedPos = pos();
    const state: PersistedState = {
      pick: pickedPath,
      track_url: current()?.url,
      track_title: current()?.title,
      pos: pos(),
      vol: srcVol(),
    };
    send({ kind: 'save_state', state });
  };
  // Debounced persist for high-frequency sources (volume drag,
  // timeupdate) so the router isn't spammed with app_state patches.
  const schedulePersist = () => {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(persist, 300);
  };
  const scan = (root?: string) => send(root ? { kind: 'scan', id: 'm-scan', root } : { kind: 'scan', id: 'm-scan' });
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
    persist();
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
        // Re-select the saved track (by URL, falling back to title)
        // when this scan is the restore-driven one.
        let idx = -1;
        if (restore) {
          idx = s.tracks.findIndex((t) => t.url === restore!.track_url);
          if (idx < 0 && restore.track_title) {
            idx = s.tracks.findIndex((t) => t.title === restore!.track_title);
          }
        }
        if (idx >= 0) {
          const t = s.tracks[idx];
          setIndex(idx);
          setSelected(idx);
          pendingSeek = restore?.pos ?? 0;
          audioEl.src = t.url;
          applyVolume();
          setStatus('paused');
          audio?.register({ title: t.title });
        } else {
          setIndex(-1);
          setSelected(s.tracks.length ? 0 : -1);
          audio?.register({ title: s.tracks[0]?.title ?? '' });
        }
        restore = null;
      }
    };
    // wash:state drives the first scan: restore the saved folder, else
    // default. Always fires on mount (null = first launch).
    const onState = (ev: Event) => {
      const s = (ev as CustomEvent).detail as PersistedState | null;
      pickedPath = s?.pick || '';
      if (s?.vol != null) setSrcVol(s.vol);
      if (s?.track_url || s?.track_title) restore = s;
      scan(pickedPath || undefined);
    };
    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('wash:state', onState);
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('wash:state', onState);
    });
  });

  onCleanup(() => {
    if (persistTimer) clearTimeout(persistTimer);
    audio?.dispose();
  });

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
              schedulePersist();
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
          pickedPath = p;
          scan(p);
          persist();
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
          persist();
        }}
        onTimeUpdate={() => {
          setPos(audioEl.currentTime);
          audio?.report();
          // Keep the saved position roughly current so a reload
          // resumes near where playback was, without persisting on
          // every 250ms tick.
          if (Math.abs(pos() - lastPersistedPos) > 5) schedulePersist();
        }}
        onLoadedMetadata={() => {
          setDur(audioEl.duration || 0);
          if (pendingSeek > 0) {
            audioEl.currentTime = pendingSeek;
            setPos(pendingSeek);
            pendingSeek = -1;
          }
        }}
        onEnded={next}
      />
    </div>
  );
}

defineWashApp('wash-app-music', MusicApp, {
  style: `display:block;width:100%;height:100%;background:${tokens.bgWindow};color:${tokens.fg};`,
});

// wash-radio FE — a minimalist NATIVE internet-radio player
// (docs/RADIO.md): a station list, basic transport, a now-playing panel.
// The BE reverse-proxies the upstream stream over ingress (same-origin, so
// no mixed-content/CORS); the FE plays it with <audio> and registers with
// com.wash.audio. Same kit as the Music app; the "list" is stations and
// the SeekBar runs in non-seekable LIVE mode. (Live ICY track metadata is
// a planned M2; now-playing is the station name for now.)

import {
  Button,
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
import { Plus, Radio } from 'lucide-solid';

interface Station {
  name: string;
  codec: string;
}
interface StationsOk {
  kind: 'stations_ok';
  base: string;
  stations: Station[];
}

function RadioApp(props: WashAppProps) {
  let audioEl!: HTMLAudioElement;
  let audio: AudioSource | undefined;
  let nonce = 0;

  const [stations, setStations] = createSignal<Station[]>([]);
  const [base, setBase] = createSignal('');
  const [index, setIndex] = createSignal(-1); // tuned station
  const [selected, setSelected] = createSignal(-1);
  const [status, setStatus] = createSignal('stopped');
  const [srcVol, setSrcVol] = createSignal(1);
  const [addUrl, setAddUrl] = createSignal('');
  const [icyTitle, setIcyTitle] = createSignal(''); // live track from ICY metadata
  let masterVol = 1;

  const current = () => stations()[index()];
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);
  const applyVolume = () => {
    if (audioEl) audioEl.volume = masterVol * srcVol();
  };

  function tune(i: number) {
    const st = stations()[i];
    if (!st || !base()) return;
    setIndex(i);
    setSelected(i);
    setIcyTitle(''); // clear the previous station's track
    nonce += 1;
    audioEl.src = `${base()}stream?i=${i}&n=${nonce}`;
    applyVolume();
    void audioEl.play().catch(() => {});
    audio?.report();
  }
  function play() {
    if (index() < 0) {
      if (stations().length) tune(0);
      return;
    }
    void audioEl.play().catch(() => {});
  }
  const pause = () => audioEl?.pause();
  const next = () => {
    const n = stations().length;
    if (n) tune((index() + 1 + n) % n);
  };
  const prev = () => {
    const n = stations().length;
    if (n) tune((index() - 1 + n) % n);
  };

  function onCmd(action: string, value?: number) {
    switch (action) {
      case 'play':
        play();
        break;
      case 'pause':
      case 'stop':
        pause();
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

  function addStation() {
    const u = addUrl().trim();
    if (!u) return;
    setAddUrl('');
    send({ kind: 'add', id: 'r-add', url: u });
  }

  onMount(() => {
    audio = createAudioSource({
      instance: props.instance,
      host: props.host,
      snapshot: () => ({ title: icyTitle() || current()?.name || '', status: status(), pos: 0, dur: 0 }),
      onCmd,
    });
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as { kind?: string; title?: string };
      if (m?.kind === 'now_playing') {
        setIcyTitle(m.title ?? '');
        audio?.report();
      } else if (m?.kind === 'stations_ok') {
        const s = m as StationsOk;
        const had = stations().length;
        setStations(s.stations);
        setBase(s.base);
        if (index() < 0) {
          setSelected(s.stations.length ? 0 : -1);
          audio?.register({ title: s.stations[0]?.name ?? '' });
        } else if (s.stations.length > had) {
          // a freshly-added station → focus it
          setSelected(s.stations.length - 1);
        }
      }
    };
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    send({ kind: 'stations', id: 'r-stations' });
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
        data-testid="radio-nowplaying"
        title={icyTitle() || current()?.name || '—'}
        subtitle={icyTitle() ? current()?.name : current() ? current()!.codec : `${stations().length} stations`}
        meta={status() === 'playing' ? '● LIVE' : ''}
      />

      <MediaList
        data-testid="station-list"
        items={stations()}
        selected={selected()}
        playing={index()}
        onSelect={setSelected}
        onActivate={tune}
        empty="No stations"
        row={(st: Station, _i, playing) => (
          <>
            <span style={{ width: '14px', 'flex-shrink': 0, display: 'inline-flex', 'align-items': 'center' }}>
              {playing ? <Radio size={11} /> : null}
            </span>
            <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
              {st.name}
            </span>
            <span style={{ color: tokens.fgMuted, 'font-size': tokens.fontSizeSm, 'flex-shrink': 0 }}>{st.codec}</span>
          </>
        )}
      />

      {/* paste a stream URL */}
      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceMd}px` }}>
        <input
          data-testid="add-url"
          type="text"
          spellcheck={false}
          placeholder="Paste a stream URL…"
          value={addUrl()}
          onInput={(e) => setAddUrl(e.currentTarget.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              addStation();
            }
          }}
          style={{
            flex: 1,
            background: tokens.bgInset,
            color: tokens.fg,
            border: `1px solid ${tokens.borderMenu}`,
            'border-radius': `${tokens.radiusSm}px`,
            padding: '4px 8px',
            'font-size': tokens.fontSizeMd,
            outline: 'none',
          }}
        />
        <Button data-testid="add-station" onClick={addStation}>
          <Plus size={13} />
        </Button>
      </div>

      <SeekBar pos={0} dur={0} seekable={false} />

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
      />
    </div>
  );
}

defineWashApp('wash-app-radio', RadioApp, {
  style: `display:block;width:100%;height:100%;background:${tokens.bgWindow};color:${tokens.fg};`,
});

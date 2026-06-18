// wash-radio FE — a minimalist NATIVE internet-radio player
// (docs/RADIO.md): a station list (curated + your pasted URLs), basic
// transport, a now-playing panel with the live ICY track, favorites, and
// offline feedback. The BE reverse-proxies the upstream stream over
// ingress (same-origin, so no mixed-content/CORS); the FE plays it with
// <audio> and registers with com.wash.audio. Same kit as the Music app;
// the "list" is stations and the SeekBar runs in non-seekable LIVE mode.
// Favorites + pasted stations + last-tuned persist via app_state.

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
import { createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { Plus, Radio, Star } from 'lucide-solid';

interface Station {
  name: string;
  codec: string;
}
interface StationsOk {
  kind: 'stations_ok';
  base: string;
  stations: Station[];
}
interface Custom {
  name: string;
  url: string;
}
interface PersistedRadio {
  custom?: Custom[];
  favs?: string[];
  last?: string;
  vol?: number;
}
type Row = Station & { be: number };

function RadioApp(props: WashAppProps) {
  let audioEl!: HTMLAudioElement;
  let audio: AudioSource | undefined;
  let nonce = 0;
  let customStations: Custom[] = []; // user-added; persisted + re-added on mount
  let lastName = ''; // last-tuned station name (persisted)
  let registered = false;

  const [stations, setStations] = createSignal<Station[]>([]);
  const [base, setBase] = createSignal('');
  const [index, setIndex] = createSignal(-1); // tuned station (BE index)
  const [selectedDisplay, setSelectedDisplay] = createSignal(-1);
  const [status, setStatus] = createSignal('stopped');
  const [srcVol, setSrcVol] = createSignal(1);
  const [addUrl, setAddUrl] = createSignal('');
  const [icyTitle, setIcyTitle] = createSignal(''); // live ICY track
  const [favs, setFavs] = createSignal<Set<string>>(new Set());
  const [offline, setOffline] = createSignal(false);
  let masterVol = 1;

  const current = () => stations()[index()];
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);
  const applyVolume = () => {
    if (audioEl) audioEl.volume = masterVol * srcVol();
  };
  let persistTimer: ReturnType<typeof setTimeout> | undefined;
  const persist = () =>
    send({
      kind: 'save_state',
      state: { custom: customStations, favs: [...favs()], last: lastName, vol: srcVol() },
    });
  // Debounced variant for the volume slider — drags fire onInput per
  // tick and each persist is a router app_state broadcast.
  const schedulePersist = () => {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(persist, 300);
  };

  // Favorites float to the top (stable); each row keeps its BE index so we
  // tune the right station regardless of display order.
  const rows = createMemo<Row[]>(() => {
    const f = favs();
    return stations()
      .map((s, i) => ({ ...s, be: i }))
      .sort((a, b) => (f.has(b.name) ? 1 : 0) - (f.has(a.name) ? 1 : 0));
  });
  const playingDisplay = () => rows().findIndex((r) => r.be === index());

  function toggleFav(name: string) {
    const f = new Set(favs());
    if (f.has(name)) f.delete(name);
    else f.add(name);
    setFavs(f);
    persist();
  }

  function tune(be: number) {
    const st = stations()[be];
    if (!st || !base()) return;
    setIndex(be);
    setSelectedDisplay(rows().findIndex((r) => r.be === be));
    setIcyTitle('');
    setOffline(false);
    lastName = st.name;
    persist();
    nonce += 1;
    audioEl.src = `${base()}stream?i=${be}&n=${nonce}`;
    applyVolume();
    void audioEl.play().catch(() => {});
    audio?.report();
  }
  function play() {
    if (index() < 0) {
      if (stations().length) tune(rows()[Math.max(0, selectedDisplay())]?.be ?? 0);
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

  const sendCustom = () => send({ kind: 'set_custom', id: 'r-set', stations: customStations });

  function addStation() {
    const u = addUrl().trim();
    if (!u) return;
    setAddUrl('');
    customStations = [...customStations, { name: u, url: u }];
    persist();
    sendCustom();
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
        setStations(s.stations);
        setBase(s.base);
        if (index() < 0) {
          // Re-select the last-tuned station once it's present (converges
          // as persisted custom stations get re-added), else the first row.
          let di = lastName ? rows().findIndex((r) => r.name === lastName) : -1;
          if (di < 0) di = s.stations.length ? 0 : -1;
          setSelectedDisplay(di);
          if (!registered && s.stations.length) {
            registered = true;
            audio?.register({ title: rows()[di]?.name ?? '' });
          }
        }
      }
    };
    // wash:state (always fires on mount, null = first launch): restore
    // favorites + pasted stations + last-tuned, then fetch the list and
    // re-add the persisted custom stations to the fresh BE.
    const onState = (ev: Event) => {
      const st = (ev as CustomEvent).detail as PersistedRadio | null;
      customStations = st?.custom ?? [];
      lastName = st?.last ?? '';
      setFavs(new Set(st?.favs ?? []));
      if (st?.vol != null) {
        setSrcVol(st.vol);
        applyVolume();
      }
      // One idempotent message both fetches the list and (re)sets the
      // pasted stations — no duplicates if the BE instance survived a reload.
      sendCustom();
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
        data-testid="radio-nowplaying"
        title={icyTitle() || current()?.name || '—'}
        subtitle={icyTitle() ? current()?.name : current() ? current()!.codec : `${stations().length} stations`}
        meta={status() === 'playing' ? '● LIVE' : offline() ? '⚠ offline' : ''}
      />

      <MediaList
        data-testid="station-list"
        items={rows()}
        selected={selectedDisplay()}
        playing={playingDisplay()}
        onSelect={setSelectedDisplay}
        onActivate={(di) => tune(rows()[di].be)}
        empty="No stations"
        row={(r: Row, _di, playing) => {
          const fav = () => favs().has(r.name);
          return (
            <>
              <span style={{ width: '14px', 'flex-shrink': 0, display: 'inline-flex', 'align-items': 'center' }}>
                {playing ? <Radio size={11} /> : null}
              </span>
              <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
                {r.name}
              </span>
              <span style={{ color: tokens.fgMuted, 'font-size': tokens.fontSizeSm, 'flex-shrink': 0 }}>{r.codec}</span>
              <span
                data-testid={`fav-${r.be}`}
                data-fav={fav() ? 'true' : undefined}
                title={fav() ? 'Unfavorite' : 'Favorite'}
                onClick={(e) => {
                  e.stopPropagation();
                  toggleFav(r.name);
                }}
                style={{
                  'flex-shrink': 0,
                  cursor: 'pointer',
                  display: 'inline-flex',
                  'align-items': 'center',
                  color: fav() ? tokens.accentAmber : tokens.fgDim,
                }}
              >
                <Star size={13} fill={fav() ? 'currentColor' : 'none'} />
              </span>
            </>
          );
        }}
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
            'border-radius': `${tokens.radiusSm}`,
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
              schedulePersist();
            }}
          />
        </div>
      </div>

      <audio
        ref={audioEl}
        style={{ display: 'none' }}
        onPlay={() => {
          setStatus('playing');
          setOffline(false);
          audio?.report();
        }}
        onPause={() => {
          setStatus('paused');
          audio?.report();
        }}
        onError={() => {
          if (index() >= 0) setOffline(true);
        }}
      />
    </div>
  );
}

defineWashApp('wash-app-radio', RadioApp, {
  style: `display:block;width:100%;height:100%;background:${tokens.bgWindow};color:${tokens.fg};`,
});

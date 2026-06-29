// wash-radio FE — a minimalist NATIVE internet-radio player
// (docs/RADIO.md): a station list (configured defaults + your pasted URLs), basic
// transport, a now-playing panel with the live ICY track, favorites, and
// offline feedback. The BE reverse-proxies the upstream stream over
// ingress (same-origin, so no mixed-content/CORS); the FE plays it with
// <audio> and registers with com.wash.audio. Same kit as the Music app;
// the "list" is stations and the SeekBar runs in non-seekable LIVE mode.
// Favorites + pasted stations + last-tuned persist via app_state.

import {
  Button,
  Input,
  NowPlaying,
  SeekBar,
  TransportControls,
  VolumeSlider,
  createAppBus,
  createAudioSource,
  defineWashApp,
  tokens,
  type AudioSource,
  type WashAppProps,
} from '@wash/ui';
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js';
import { ChevronDown, ChevronRight, Info, Plus, Radio, Star } from 'lucide-solid';

interface Station {
  name: string;
  codec: string;
  genre?: string;
  subtype?: string;
  source?: string;
  description?: string;
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
interface StreamInfo {
  content_type?: string;
  bitrate?: string;
  icy_name?: string;
  icy_genre?: string;
  icy_url?: string;
  icy_description?: string;
  meta_interval?: number;
}
type Row = Station & { be: number };
type SubtypeGroup = { subtype: string; rows: Row[] };
type GenreGroup = { genre: string; rows: Row[]; subtypes: SubtypeGroup[] };
type VisibleEntry =
  | { kind: 'genre'; genre: string; count: number; collapsed: boolean; key: string }
  | { kind: 'subtype'; genre: string; subtype: string; count: number; collapsed: boolean; key: string }
  | { kind: 'station'; row: Row; di: number; depth: number };
type InfoItem = { label: string; value: string; wide?: boolean };

const genreOrder = [
  'Electronic',
  'Ambient',
  'Rock',
  'Pop',
  'Hip-Hop / R&B',
  'Country / Americana',
  'Latin / World',
  'Jazz',
  'Classical',
  'Reggae / Ska',
  'Metal',
  'Folk',
  'Oldies / Soul',
  'Lounge',
  'Eclectic',
  'Custom',
  'Other',
];
const subtypeOrder: Record<string, string[]> = {
  Electronic: [
    'Hacker / Cyberpunk',
    'Industrial / Dark Ambient',
    'Downtempo / Chill',
    'Deep House',
    'Progressive / Trance',
    'IDM',
    'Drum & Bass',
    'Dubstep / Bass',
    'Electropop / Synthpop',
    'Vaporwave',
    'Ambient / Space',
    'Techno',
  ],
  Ambient: ['Drone', 'Deep Ambient', 'Space Ambient', 'Dark Ambient'],
  Rock: ['Alternative / Modern', 'Indie Rock', 'Punk Rock', 'Hard Rock', 'Nu-Metal'],
  Metal: ['Metal', 'Heavy Metal'],
};
const genreRank = (genre: string) => {
  const i = genreOrder.indexOf(genre);
  return i < 0 ? genreOrder.length : i;
};
const subtypeRank = (genre: string, subtype: string) => {
  if (!subtype) return -1;
  const order = subtypeOrder[genre];
  if (!order) return Number.MAX_SAFE_INTEGER;
  const i = order.indexOf(subtype);
  return i < 0 ? order.length : i;
};
const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
const groupKey = (kind: 'genre' | 'subtype', genre: string, subtype = '') => `${kind}:${genre}:${subtype}`;

function collapsedTree(groups: GenreGroup[], openRow?: Row): Set<string> {
  const closed = new Set<string>();
  const openGenre = openRow ? inferredGenre(openRow) : '';
  const openSubtype = openRow ? inferredSubtype(openRow) : '';
  for (const group of groups) {
    const keepGenreOpen = group.genre === openGenre;
    if (!keepGenreOpen) closed.add(groupKey('genre', group.genre));
    for (const sub of group.subtypes) {
      if (!keepGenreOpen || sub.subtype !== openSubtype) closed.add(groupKey('subtype', group.genre, sub.subtype));
    }
  }
  return closed;
}

function inferredGenre(st: Station): string {
  if (st.genre) return st.genre;
  const label = `${st.codec} ${st.name}`.toLowerCase();
  if (label.includes('ambient') || label.includes('drone') || label.includes('space')) return 'Ambient';
  if (label.includes('metal')) return 'Metal';
  if (label.includes('rock') || label.includes('80s')) return 'Rock';
  if (label.includes('lounge')) return 'Lounge';
  if (label.includes('stream')) return 'Custom';
  if (label.includes('chill') || label.includes('house') || label.includes('idm') || label.includes('electro') || label.includes('dnb') || label.includes('dubstep')) return 'Electronic';
  return 'Other';
}

function inferredSubtype(st: Station): string {
  if (st.subtype) return st.subtype;
  const genre = inferredGenre(st);
  const label = `${st.codec} ${st.name}`.toLowerCase();
  if (genre === 'Electronic') {
    if (label.includes('dnb') || label.includes('drum')) return 'Drum & Bass';
    if (label.includes('dubstep') || label.includes('bass')) return 'Dubstep / Bass';
    if (label.includes('idm')) return 'IDM';
    if (label.includes('trance') || label.includes('prog')) return 'Progressive / Trance';
    if (label.includes('house')) return 'Deep House';
    if (label.includes('electropop') || label.includes('synth')) return 'Electropop / Synthpop';
    if (label.includes('vapor')) return 'Vaporwave';
    if (label.includes('space')) return 'Ambient / Space';
    if (label.includes('techno')) return 'Techno';
    if (label.includes('chill')) return 'Downtempo / Chill';
  }
  if (genre === 'Rock') {
    if (label.includes('nu')) return 'Nu-Metal';
    if (label.includes('punk')) return 'Punk Rock';
    if (label.includes('hard')) return 'Hard Rock';
    if (label.includes('indie')) return 'Indie Rock';
    if (label.includes('alternative') || label.includes('modern')) return 'Alternative / Modern';
  }
  return '';
}

function splitTrack(title: string): { artist?: string; track?: string } {
  const clean = title.trim();
  if (!clean) return {};
  const parts = clean.split(/\s+-\s+/);
  if (parts.length >= 2) return { artist: parts[0], track: parts.slice(1).join(' - ') };
  return { track: clean };
}

function cleanContentType(ct = ''): string {
  return ct.split(';', 1)[0]?.trim() ?? ct;
}

function bitrateLabel(br = ''): string {
  const clean = br.trim();
  if (!clean) return '';
  return /^\d+$/.test(clean) ? `${clean} kbps` : clean;
}

function RadioApp(props: WashAppProps) {
  let audioEl!: HTMLAudioElement;
  let audio: AudioSource | undefined;
  let nonce = 0;
  let customStations: Custom[] = []; // user-added; persisted + re-added on mount
  let lastName = ''; // last-tuned station name (persisted)
  let registered = false;
  let treeInitialized = false;
  let revealedLastName = '';
  let pendingRevealName = '';

  const [stations, setStations] = createSignal<Station[]>([]);
  const [base, setBase] = createSignal('');
  const [index, setIndex] = createSignal(-1); // tuned station (BE index)
  const [selectedDisplay, setSelectedDisplay] = createSignal(-1);
  const [status, setStatus] = createSignal('stopped');
  const [srcVol, setSrcVol] = createSignal(1);
  const [addUrl, setAddUrl] = createSignal('');
  const [icyTitle, setIcyTitle] = createSignal(''); // live ICY track
  const [streamInfo, setStreamInfo] = createSignal<StreamInfo>({});
  const [favs, setFavs] = createSignal<Set<string>>(new Set());
  const [offline, setOffline] = createSignal(false);
  const [collapsedGroups, setCollapsedGroups] = createSignal<Set<string>>(new Set());
  let masterVol = 1;

  const current = () => stations()[index()];
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

  // Each row keeps its BE index so tree sorting/collapse never changes the
  // backend station address used by /stream?i=N.
  const stationRows = createMemo<Row[]>(() => {
    const f = favs();
    return stations().map((s, i) => ({ ...s, be: i })).sort((a, b) => {
      const genreA = inferredGenre(a);
      const genreB = inferredGenre(b);
      const ga = genreRank(genreA);
      const gb = genreRank(genreB);
      if (ga !== gb) return ga - gb;
      const fg = genreA.localeCompare(genreB);
      if (fg !== 0) return fg;
      const subA = inferredSubtype(a);
      const subB = inferredSubtype(b);
      const sa = subtypeRank(genreA, subA);
      const sb = subtypeRank(genreB, subB);
      if (sa !== sb) return sa - sb;
      const fs = subA.localeCompare(subB);
      if (fs !== 0) return fs;
      const fav = (f.has(b.name) ? 1 : 0) - (f.has(a.name) ? 1 : 0);
      return fav || a.be - b.be;
    });
  });
  const groupedRows = createMemo<GenreGroup[]>(() => {
    const groups = new Map<string, { rows: Row[]; subtypes: Map<string, Row[]> }>();
    for (const row of stationRows()) {
      const genre = inferredGenre(row);
      let group = groups.get(genre);
      if (!group) {
        group = { rows: [], subtypes: new Map() };
        groups.set(genre, group);
      }
      const subtype = inferredSubtype(row);
      if (!subtype) {
        group.rows.push(row);
        continue;
      }
      const rows = group.subtypes.get(subtype);
      if (rows) rows.push(row);
      else group.subtypes.set(subtype, [row]);
    }
    return [...groups.entries()]
      .sort(([genreA], [genreB]) => {
        const rank = genreRank(genreA) - genreRank(genreB);
        return rank || genreA.localeCompare(genreB);
      })
      .map(([genre, group]) => ({
        genre,
        rows: group.rows,
        subtypes: [...group.subtypes.entries()]
          .sort(([subA], [subB]) => {
            const rank = subtypeRank(genre, subA) - subtypeRank(genre, subB);
            return rank || subA.localeCompare(subB);
          })
          .map(([subtype, rows]) => ({ subtype, rows })),
      }));
  });
  const visibleEntries = createMemo<VisibleEntry[]>(() => {
    const closed = collapsedGroups();
    const entries: VisibleEntry[] = [];
    let di = 0;
    for (const group of groupedRows()) {
      const key = groupKey('genre', group.genre);
      const count = group.rows.length + group.subtypes.reduce((n, sub) => n + sub.rows.length, 0);
      const collapsed = closed.has(key);
      entries.push({ kind: 'genre', genre: group.genre, count, collapsed, key });
      if (collapsed) continue;
      for (const row of group.rows) entries.push({ kind: 'station', row, di: di++, depth: 1 });
      for (const sub of group.subtypes) {
        const subKey = groupKey('subtype', group.genre, sub.subtype);
        const subCollapsed = closed.has(subKey);
        entries.push({ kind: 'subtype', genre: group.genre, subtype: sub.subtype, count: sub.rows.length, collapsed: subCollapsed, key: subKey });
        if (!subCollapsed) {
          for (const row of sub.rows) entries.push({ kind: 'station', row, di: di++, depth: 2 });
        }
      }
    }
    return entries;
  });
  const visibleRows = createMemo<Row[]>(() => visibleEntries().filter((e): e is { kind: 'station'; row: Row; di: number } => e.kind === 'station').map((e) => e.row));
  const playingDisplay = () => visibleRows().findIndex((r) => r.be === index());

  function toggleFav(name: string) {
    const f = new Set(favs());
    if (f.has(name)) f.delete(name);
    else f.add(name);
    setFavs(f);
    persist();
  }

  function toggleGroup(key: string) {
    const next = new Set(collapsedGroups());
    if (next.has(key)) next.delete(key);
    else next.add(key);
    setCollapsedGroups(next);
    const rows = visibleRows();
    if (selectedDisplay() >= rows.length) setSelectedDisplay(rows.length ? rows.length - 1 : -1);
  }

  function openPathToRow(row: Row): number {
    const next = new Set(collapsedGroups());
    const genre = inferredGenre(row);
    const subtype = inferredSubtype(row);
    next.delete(groupKey('genre', genre));
    if (subtype) next.delete(groupKey('subtype', genre, subtype));
    setCollapsedGroups(next);
    const di = visibleRows().findIndex((r) => r.be === row.be);
    setSelectedDisplay(di);
    return di;
  }

  function revealStation(be: number): number {
    const row = stationRows().find((r) => r.be === be);
    if (row) return openPathToRow(row);
    const di = visibleRows().findIndex((r) => r.be === be);
    setSelectedDisplay(di);
    return di;
  }

  function tune(be: number) {
    const st = stations()[be];
    if (!st || !base()) return;
    setIndex(be);
    revealStation(be);
    setIcyTitle('');
    setStreamInfo({});
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
      if (stations().length) tune(visibleRows()[Math.max(0, selectedDisplay())]?.be ?? 0);
      return;
    }
    void audioEl.play().catch(() => {});
  }
  const pause = () => audioEl?.pause();
  const step = (delta: number) => {
    const rows = stationRows();
    if (!rows.length) return;
    let pos = rows.findIndex((r) => r.be === index());
    if (pos < 0) {
      const selected = visibleRows()[Math.max(0, selectedDisplay())];
      pos = selected ? rows.findIndex((r) => r.be === selected.be) : 0;
    }
    const row = rows[(pos + delta + rows.length) % rows.length];
    if (row) tune(row.be);
  };
  const next = () => step(1);
  const prev = () => step(-1);

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

  const onListKey = (e: KeyboardEvent) => {
    const n = visibleRows().length;
    if (!n) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelectedDisplay(Math.min(n - 1, selectedDisplay() + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelectedDisplay(Math.max(0, (selectedDisplay() < 0 ? 0 : selectedDisplay()) - 1));
    } else if (e.key === 'Enter' && selectedDisplay() >= 0) {
      e.preventDefault();
      tune(visibleRows()[selectedDisplay()].be);
    }
  };

  function addStation() {
    const u = addUrl().trim();
    if (!u) return;
    setAddUrl('');
    customStations = [...customStations, { name: u, url: u }];
    pendingRevealName = u;
    persist();
    sendCustom();
  }

  const handleBE = (m: { kind?: string; title?: string; station?: number; info?: StreamInfo }) => {
    if (m?.kind === 'now_playing') {
      if (m.station == null || m.station === index()) {
        setIcyTitle(m.title ?? '');
        audio?.report();
      }
    } else if (m?.kind === 'stream_info') {
      if (m.station == null || m.station === index()) setStreamInfo(m.info ?? {});
    } else if (m?.kind === 'stations_ok') {
      const s = m as StationsOk;
      setStations(s.stations);
      setBase(s.base);
      const rows = stationRows();
      const pendingRow = pendingRevealName ? rows.find((r) => r.name === pendingRevealName) : undefined;
      const lastRow = lastName ? rows.find((r) => r.name === lastName) : undefined;
      const shouldRevealLast = !!lastRow && index() < 0 && revealedLastName !== lastName;

      if (pendingRow) {
        if (!treeInitialized) {
          setCollapsedGroups(collapsedTree(groupedRows(), pendingRow));
          treeInitialized = true;
        } else {
          openPathToRow(pendingRow);
        }
        setSelectedDisplay(visibleRows().findIndex((r) => r.be === pendingRow.be));
        pendingRevealName = '';
      } else if (!treeInitialized || shouldRevealLast) {
        // Launch default: collapse the whole tree, except the restored
        // last-played station's genre/subtype path when it exists.
        setCollapsedGroups(collapsedTree(groupedRows(), lastRow));
        treeInitialized = true;
        if (lastRow) {
          revealedLastName = lastName;
          setSelectedDisplay(visibleRows().findIndex((r) => r.be === lastRow.be));
        } else {
          setSelectedDisplay(-1);
        }
      }

      if (index() < 0) {
        const visible = visibleRows();
        if (selectedDisplay() >= visible.length) setSelectedDisplay(visible.length ? visible.length - 1 : -1);
        if (!registered && s.stations.length) {
          registered = true;
          audio?.register({ title: lastRow?.name ?? rows[0]?.name ?? '' });
        }
      }
    }
  };
  // wash:state (always fires on mount, null = first launch): restore
  // favorites + pasted stations + last-tuned, then fetch the list and
  // re-add the persisted custom stations to the fresh BE.
  const handleState = (st: PersistedRadio | null) => {
    customStations = st?.custom ?? [];
    lastName = st?.last ?? '';
    treeInitialized = false;
    revealedLastName = '';
    pendingRevealName = '';
    setFavs(new Set(st?.favs ?? []));
    if (st?.vol != null) {
      setSrcVol(st.vol);
      applyVolume();
    }
    // One idempotent message both fetches the list and (re)sets the
    // pasted stations — no duplicates if the BE instance survived a reload.
    sendCustom();
  };

  const { send } = createAppBus(props, {
    onMsg: handleBE,
    onState: (s) => handleState(s as PersistedRadio | null),
  });

  const infoItems = createMemo<InfoItem[]>(() => {
    const st = current();
    const info = streamInfo();
    const title = splitTrack(icyTitle());
    const items: InfoItem[] = [];
    if (title.artist) items.push({ label: 'Artist', value: title.artist });
    if (title.track) items.push({ label: 'Track', value: title.track });
    if (st?.genre) items.push({ label: 'Genre', value: st.genre });
    if (st?.subtype) items.push({ label: 'Subtype', value: st.subtype });
    if (st?.codec) items.push({ label: 'Style', value: st.codec });
    if (st?.source) items.push({ label: 'Source', value: st.source });
    if (info.bitrate) items.push({ label: 'Bitrate', value: bitrateLabel(info.bitrate) });
    if (info.content_type) items.push({ label: 'Stream', value: cleanContentType(info.content_type) });
    if (info.icy_name && info.icy_name !== st?.name) items.push({ label: 'ICY name', value: info.icy_name });
    if (info.icy_genre && info.icy_genre !== st?.codec) items.push({ label: 'ICY genre', value: info.icy_genre });
    if (info.meta_interval) items.push({ label: 'ICY metadata', value: `${info.meta_interval} bytes` });
    if (info.icy_url) items.push({ label: 'Web', value: info.icy_url, wide: true });
    if (info.icy_description) items.push({ label: 'About', value: info.icy_description, wide: true });
    else if (st?.description) items.push({ label: 'About', value: st.description, wide: true });
    if (!items.length) items.push({ label: 'Library', value: `${stations().length} stations` });
    return items;
  });

  onMount(() => {
    audio = createAudioSource({
      instance: props.instance,
      host: props.host,
      snapshot: () => ({ title: icyTitle() || current()?.name || '', status: status(), pos: 0, dur: 0 }),
      onCmd,
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
        meta={status() === 'playing' ? 'LIVE' : offline() ? 'offline' : ''}
      />

      <div
        data-testid="radio-stream-info"
        style={{
          background: tokens.bgNeutral,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': `${tokens.radiusSm}`,
          padding: '8px 10px',
          display: 'flex',
          'flex-direction': 'column',
          gap: '6px',
          'flex-shrink': 0,
        }}
      >
        <div style={{ display: 'flex', 'align-items': 'center', gap: '6px', color: tokens.fgMuted, 'font-size': tokens.fontSizeSm }}>
          <Info size={12} />
          <span>Stream</span>
        </div>
        <div style={{ display: 'grid', 'grid-template-columns': 'repeat(auto-fit, minmax(120px, 1fr))', gap: '5px 10px' }}>
          <For each={infoItems()}>
            {(item) => (
              <div
                title={item.value}
                style={{
                  'grid-column': item.wide ? '1 / -1' : undefined,
                  overflow: 'hidden',
                  'min-width': 0,
                }}
              >
                <div style={{ color: tokens.fgDim, 'font-size': tokens.fontSizeSm, 'line-height': 1.2 }}>{item.label}</div>
                <div
                  style={{
                    color: tokens.fg,
                    'font-size': tokens.fontSizeSm,
                    overflow: 'hidden',
                    'text-overflow': 'ellipsis',
                    'white-space': 'nowrap',
                  }}
                >
                  {item.value}
                </div>
              </div>
            )}
          </For>
        </div>
      </div>

      <div
        data-testid="station-list"
        tabindex={0}
        onKeyDown={onListKey}
        style={{
          flex: 1,
          'min-height': 0,
          overflow: 'auto',
          background: tokens.bgNeutral,
          'border-radius': `${tokens.radiusSm}`,
          outline: 'none',
        }}
      >
        <Show
          when={stations().length}
          fallback={<div style={{ padding: '16px', color: tokens.fgDim, 'font-style': 'italic', 'font-size': tokens.fontSizeSm }}>No stations</div>}
        >
          <For each={visibleEntries()}>
            {(entry) =>
              entry.kind === 'genre' ? (
                <button
                  type="button"
                  data-testid={`genre-${slug(entry.genre)}`}
                  aria-expanded={!entry.collapsed}
                  onClick={() => toggleGroup(entry.key)}
                  style={{
                    width: '100%',
                    display: 'flex',
                    'align-items': 'center',
                    gap: '7px',
                    padding: '6px 9px',
                    background: tokens.bgInset,
                    color: tokens.fgMuted,
                    border: 0,
                    'border-top': `1px solid ${tokens.borderMenu}`,
                    cursor: 'pointer',
                    'font-size': tokens.fontSizeSm,
                    'text-align': 'left',
                  }}
                >
                  {entry.collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
                  <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{entry.genre}</span>
                  <span style={{ color: tokens.fgDim, 'font-variant': 'tabular-nums' }}>{entry.count}</span>
                </button>
              ) : entry.kind === 'subtype' ? (
                <button
                  type="button"
                  data-testid={`subtype-${slug(entry.genre)}-${slug(entry.subtype)}`}
                  aria-expanded={!entry.collapsed}
                  onClick={() => toggleGroup(entry.key)}
                  style={{
                    width: '100%',
                    display: 'flex',
                    'align-items': 'center',
                    gap: '7px',
                    padding: '5px 9px 5px 22px',
                    background: tokens.bgNeutral,
                    color: tokens.fgMuted,
                    border: 0,
                    'border-top': `1px solid ${tokens.borderMenu}`,
                    cursor: 'pointer',
                    'font-size': tokens.fontSizeSm,
                    'text-align': 'left',
                  }}
                >
                  {entry.collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
                  <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{entry.subtype}</span>
                  <span style={{ color: tokens.fgDim, 'font-variant': 'tabular-nums' }}>{entry.count}</span>
                </button>
              ) : (
                (() => {
                  const r = entry.row;
                  const selected = () => selectedDisplay() === entry.di;
                  const playing = () => playingDisplay() === entry.di;
                  const fav = () => favs().has(r.name);
                  return (
                    <div
                      data-testid={`media-row-${entry.di}`}
                      data-selected={selected() ? 'true' : undefined}
                      data-playing={playing() ? 'true' : undefined}
                      onClick={() => setSelectedDisplay(entry.di)}
                      onDblClick={() => tune(r.be)}
                      style={{
                        display: 'flex',
                        'align-items': 'center',
                        gap: '8px',
                        padding: `4px 10px 4px ${entry.depth > 1 ? 30 : 18}px`,
                        cursor: 'default',
                        'user-select': 'none',
                        'border-left': `2px solid ${playing() ? tokens.accentGreen : 'transparent'}`,
                        background: selected() ? tokens.bgRowSelected : 'transparent',
                        color: playing() ? tokens.accentGreen : tokens.fg,
                        'font-size': tokens.fontSizeBase,
                      }}
                    >
                      <span style={{ width: '14px', 'flex-shrink': 0, display: 'inline-flex', 'align-items': 'center' }}>
                        {playing() ? <Radio size={11} /> : null}
                      </span>
                      <span style={{ flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>{r.name}</span>
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
                    </div>
                  );
                })()
              )
            }
          </For>
        </Show>
      </div>

      {/* paste a stream URL */}
      <div style={{ display: 'flex', 'align-items': 'center', gap: `${tokens.spaceMd}px` }}>
        <Input
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
          style={{ flex: 1 }}
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

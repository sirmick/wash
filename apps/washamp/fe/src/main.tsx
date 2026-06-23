// wash-washamp FE — embeds Webamp (the JS classic-Winamp skin engine)
// inside a wash window, feeds it Case-1 tracks served by the BE over the
// ingress proxy (docs/AUDIO.md §1, §2), and bridges playback to the
// com.wash.audio control plane (§3): it reports now-playing/status/
// position up to the service (→ sidebar) and obeys transport/volume
// commands coming back down.

import { createAppBus, createAudioSource, defineWashApp, type AudioSource, type WashAppProps } from '@wash/ui';
import { onCleanup, onMount } from 'solid-js';
import Webamp from 'webamp';

interface TracksOk {
  kind: 'tracks_ok';
  // URLs are fully resolved by the BE (ingress path for local files,
  // absolute for streams), so the FE feeds them straight to webamp.
  tracks: { url: string; title: string; artist?: string }[];
  // Bundled classic skins served over ingress; index 0 is the default
  // (initialSkin), all populate Webamp's Options → Skins menu.
  skins?: { name: string; url: string }[];
}

// Saved FE state (wash:state round-trip): current track, position,
// volume (Webamp's 0–100 scale), shuffle/repeat. Restored to a
// paused-at-track player — the saved position is applied on the
// first play (seeking unloaded media is a no-op in Webamp).
// Skin choice is NOT here: Webamp loads skin data and discards the
// source URL, and exposes no skin-change event, so the user's pick
// is unknowable from the public API. Known gap.
interface PersistedWashamp {
  track_url?: string;
  pos?: number;
  vol?: number;
  shuffle?: boolean;
  repeat?: boolean;
}

function WashampApp(props: WashAppProps) {
  let container!: HTMLDivElement;
  let webamp: Webamp | undefined;
  // Producer bridge to com.wash.audio (register/report/transport), shared
  // by every wash media app. Created in onMount; drives + is driven by the
  // sidebar.
  let audio: AudioSource | undefined;
  // Webamp's root (#webamp), reparented into our slot (see initWebamp).
  let webampEl: HTMLElement | null = null;
  // Latest current-track identity, captured from onTrackDidChange (the
  // store doesn't carry per-track duration, so we match by url below).
  let cur = { url: '', title: '', artist: '' };
  // BE-provided labels keyed by url. Webamp rewrites a playlist track's
  // title to "Unknown" once it reads a tag-less file's (missing) ID3
  // tags, so for now-playing we anchor to the BE label (the filename
  // stem / stream label) instead of trusting Webamp's read-back.
  const trackMeta = new Map<string, { title: string; artist: string }>();
  // Restore bookkeeping: the wash:state blob held until tracks_ok
  // builds the playlist, and the saved position applied on first play.
  let saved: PersistedWashamp | null = null;
  let pendingSeek = -1;
  let lastPersisted: PersistedWashamp = {};
  let persistTimer: ReturnType<typeof setTimeout> | undefined;
  // Last volume (0..100) we've synced with the audio service, used to tell a
  // local change (user moved the Webamp knob → report it up so the sidebar
  // tracks) apart from a service-driven one (handleCmd set it → don't bounce
  // it back). -1 until the first sync.
  let lastVolume = -1;

  // readPersistable pulls the current playback blob out of Webamp.
  // Guarded like snapshot(): the store shape is not a stable contract.
  function readPersistable(wa: Webamp): PersistedWashamp {
    const out: PersistedWashamp = { track_url: cur.url, pos: 0, vol: 100 };
    try {
      const media = wa.store.getState().media as { timeElapsed?: number; volume?: number };
      out.pos = media.timeElapsed ?? 0;
      out.vol = media.volume ?? 100;
      out.shuffle = wa.isShuffleEnabled();
      out.repeat = wa.isRepeatEnabled();
    } catch {
      /* keep partial blob */
    }
    return out;
  }

  // maybePersist saves when something durable actually changed —
  // __onStateChange fires ~per-second during playback, so position
  // changes are persisted only past a 5s drift. Debounced so bursts
  // (volume drag) coalesce into one app_state write.
  function maybePersist() {
    const wa = webamp;
    if (!wa) return;
    const s = readPersistable(wa);
    if (
      s.track_url === lastPersisted.track_url &&
      Math.abs((s.pos ?? 0) - (lastPersisted.pos ?? 0)) < 5 &&
      s.vol === lastPersisted.vol &&
      s.shuffle === lastPersisted.shuffle &&
      s.repeat === lastPersisted.repeat
    ) {
      return;
    }
    lastPersisted = s;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => send({ kind: 'save_state', state: s }), 300);
  }

  // The window is chromeless (apps/washamp/be/app.go sets
  // WindowHints.Chromeless): there is no wash titlebar, so Webamp's own
  // Winamp titlebar is the single titlebar and must drive the wash
  // window. windowID is the id the shell stamps on the host element at
  // mount (data-wash-window); we use it to call window.wash.{move,close,
  // minimize}Window from Webamp's native chrome.
  const windowID = Number(props.host.getAttribute('data-wash-window') || 0);
  // Window ids are per-router; address this window by (origin, id) so a remote
  // Washamp doesn't drive a local window of the same id (docs/REMOTE.md R2).
  const origin = props.origin;
  const winInfo = () => window.wash.windows().find((w) => w.windowID === windowID && w.origin === origin);

  // DPI handling. The Winamp skin is bitmap pixel-art, so it's only
  // naturally crisp at an integer device-pixel scale; on a fractional-DPI
  // display (e.g. 1.5x) the native size maps to a non-integer device grid
  // and the pixels double unevenly. Two modes:
  //   'crisp'  — counter-scale #webamp so pixels land on an integer device
  //              multiple (round(dpr): 1x / 2x). Pixel-perfect, but the
  //              player changes physical size at fractional DPI.
  //   'smooth' — keep native size; render pixel-perfect at integer DPI and
  //              fall back to smooth (anti-aliased) interpolation only at
  //              fractional DPI. Never resizes; slightly soft at 1.5x.
  const SCALE_MODE: 'crisp' | 'smooth' = 'crisp';
  let snapDpr = 0;
  function applyDpiSnap() {
    const main = document.getElementById('main-window');
    if (!webampEl || !main) return;
    const dpr = window.devicePixelRatio || 1;
    snapDpr = dpr;
    const integerDpi = Math.abs(dpr - Math.round(dpr)) < 0.01;
    // 'crisp' counter-scales fractional DPI onto an integer device grid;
    // 'smooth' always renders at native size.
    const k = SCALE_MODE === 'crisp' ? Math.max(1, Math.round(dpr)) / dpr : 1;
    webampEl.style.transformOrigin = '0 0';
    webampEl.style.transform = k === 1 ? 'none' : `scale(${k})`;
    // In 'smooth' mode, drop Webamp's image-rendering:pixelated so a
    // fractional-DPI upscale is anti-aliased instead of blockily doubled.
    // At integer DPI native rendering is already exact, so keep it crisp.
    setSmoothRendering(SCALE_MODE === 'smooth' && !integerDpi);
    // Re-pin the (now scaled) main window to the slot's top-left.
    const cr = container.getBoundingClientRect();
    const mr = main.getBoundingClientRect();
    const curLeft = parseFloat(webampEl.style.left) || 0;
    const curTop = parseFloat(webampEl.style.top) || 0;
    webampEl.style.left = `${Math.round(curLeft - (mr.left - cr.left))}px`;
    webampEl.style.top = `${Math.round(curTop - (mr.top - cr.top))}px`;
    // Hug the (possibly scaled) main window so the chromeless frame matches.
    if (windowID) window.wash.resizeWindow(windowID, Math.round(mr.width), Math.round(mr.height), origin);
  }
  // Toggle a one-off stylesheet that forces smooth image-rendering over
  // Webamp's pixelated default across the whole #webamp subtree.
  let smoothStyle: HTMLStyleElement | null = null;
  function setSmoothRendering(on: boolean) {
    if (on && !smoothStyle) {
      smoothStyle = document.createElement('style');
      smoothStyle.textContent =
        '#webamp, #webamp * { image-rendering: auto !important; }';
      document.head.appendChild(smoothStyle);
    } else if (!on && smoothStyle) {
      smoothStyle.remove();
      smoothStyle = null;
    }
  }
  const onWindowResize = () => {
    if (window.devicePixelRatio !== snapDpr) applyDpiSnap();
  };

  // Translate a drag on Webamp's main-window titlebar into a wash window
  // move. Bound in the CAPTURE phase on the host so it runs before
  // Webamp's own (React, mousedown-based) drag handler, which we then
  // suppress with stopImmediatePropagation — otherwise the Winamp window
  // would slide around inside the frame instead of moving the frame.
  // Titlebar controls (clutterbar, options menu, minimize, shade, close)
  // are excluded so they keep working and Webamp handles them natively.
  function onHostMouseDown(e: MouseEvent) {
    const t = e.target as HTMLElement;
    // Winamp minimize → minimize the wash window. Webamp's own onMinimize
    // doesn't fire for the classic-skin button, and there's no wash
    // titlebar to minimize from, so we drive it ourselves (restore via the
    // taskbar). #shade (window-shade roll-up) and #close stay with Webamp.
    if (t.closest('#minimize') && windowID) {
      e.preventDefault();
      e.stopImmediatePropagation();
      window.wash.minimizeWindow(windowID, origin);
      return;
    }
    if (!t.closest('#title-bar')) return;
    if (t.closest('#clutter-bar, #option, #shade, #close')) return;
    const info = winInfo();
    if (!info || !windowID) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    window.wash.focusWindow(windowID, origin);
    const sx = e.clientX;
    const sy = e.clientY;
    const ox = info.x;
    const oy = info.y;
    // Clamp to the virtual-desktop plane (VIEWPORTS_PER_AXIS² = 3² cells)
    // so the window can't be dragged fully off-screen — the same bound
    // the shell's own titlebar drag applies.
    const maxX = window.innerWidth * 3 - info.w;
    const maxY = window.innerHeight * 3 - info.h;
    let raf = 0;
    let nx = ox;
    let ny = oy;
    // Coalesce moves to one in-flight animation frame; the resize handle
    // streams geometry the same way (web/shell/src/window.tsx).
    const flush = () => {
      raf = 0;
      window.wash.moveWindow(windowID, nx, ny, origin);
    };
    const onMove = (m: MouseEvent) => {
      nx = Math.round(Math.max(0, Math.min(maxX, ox + (m.clientX - sx))));
      ny = Math.round(Math.max(0, Math.min(maxY, oy + (m.clientY - sy))));
      if (!raf) raf = requestAnimationFrame(flush);
    };
    const onUp = () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      if (raf) cancelAnimationFrame(raf);
      window.wash.moveWindow(windowID, nx, ny, origin);
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  }

  // snapshot reads webamp's public store + getters into our report shape.
  // Everything is guarded: webamp's internal state shape is not a stable
  // contract, and a bad read must never break playback or reporting.
  function snapshot(wa: Webamp) {
    let status = 'stopped';
    let pos = 0;
    let dur = 0;
    let title = cur.title;
    let artist = cur.artist;
    try {
      status = wa.getMediaStatus().toLowerCase(); // PLAYING|PAUSED|STOPPED
      pos = wa.store.getState().media.timeElapsed ?? 0;
      const t = wa.getPlaylistTracks().find((x) => x.url === cur.url);
      if (t) dur = (t as { duration?: number }).duration ?? 0;
      // Prefer the BE label, then our last-known, then Webamp's read-back
      // (which may be an empty/"Unknown" tag) — `||` so empties don't win.
      const be = trackMeta.get(cur.url);
      title = be?.title || cur.title || t?.title || title;
      artist = be?.artist || cur.artist || t?.artist || artist;
    } catch {
      /* ignore — keep last-known fields */
    }
    return { title, artist, status, pos, dur };
  }

  // Throttled report via the shared audio source (coalesces the ~per-second
  // store ticks). audio is created in onMount; calls before then no-op.
  const scheduleReport = () => audio?.report();

  // A transport/volume command relayed from the service (sidebar).
  function handleCmd(action: string, value?: number) {
    const wa = webamp;
    if (!wa) return;
    switch (action) {
      case 'play':
        wa.play();
        break;
      case 'pause':
        wa.pause();
        break;
      case 'stop':
        wa.stop();
        break;
      case 'next':
        wa.nextTrack();
        break;
      case 'prev':
        wa.previousTrack();
        break;
      case 'volume':
        if (typeof value === 'number') {
          // Record before applying: the resulting onStateChange must see
          // this as already-synced and not report it back to the service.
          lastVolume = Math.round(value * 100);
          wa.setVolume(lastVolume);
        }
        break;
    }
    scheduleReport();
  }

  // syncVolumeToService pushes a locally-driven Webamp volume change up to
  // the control plane so the sidebar's master slider tracks it. Only fires
  // when the volume differs from what we last synced (lastVolume), so a
  // service-driven setVolume — or any non-volume store tick — is a no-op.
  function syncVolumeToService() {
    const wa = webamp;
    if (!wa || !audio) return;
    let v: number;
    try {
      v = (wa.store.getState().media as { volume?: number }).volume ?? 100;
    } catch {
      return;
    }
    if (v === lastVolume) return;
    lastVolume = v;
    audio.reportVolume(v / 100);
  }

  async function initWebamp(m: TracksOk) {
    if (webamp) return; // single Winamp per window
    // Open with the BE's first bundled skin and expose the rest in the
    // Options → Skins menu. Falls back to Webamp's built-in skin if the
    // BE sent none.
    const skins = m.skins ?? [];
    const wa = new Webamp(
      skins.length
        ? { initialSkin: { url: skins[0].url }, availableSkins: skins }
        : undefined,
    );
    webamp = wa;
    // Closing the Winamp player closes the wash window. (Minimize is
    // handled in onHostMouseDown — onMinimize doesn't fire for the
    // classic-skin button.)
    wa.onClose(() => windowID && window.wash.closeWindow(windowID, origin));
    await wa.renderWhenReady(container);
    // Webamp appends its root (#webamp) to <body> and floats its windows
    // as body overlays — it does not render into the node we passed. Pull
    // it into our window slot so the Winamp UI lives INSIDE the wash
    // window (pans with the viewport, obeys z-order, minimizes with the
    // window, and our titlebar-drag interception sees the events).
    webampEl = document.getElementById('webamp');
    if (webampEl) {
      container.appendChild(webampEl);
      webampEl.style.position = 'absolute';
      webampEl.style.left = '0';
      webampEl.style.top = '0';
      // Counter-scale for the display DPI and pin the main window to the
      // slot top-left; the EQ/playlist stack follows below and the whole
      // #webamp moves as one with the wash window.
      applyDpiSnap();
    }
    const tracks = m.tracks.map((t) => ({
      url: t.url,
      metaData: { title: t.title, artist: t.artist ?? '' },
    }));
    for (const t of m.tracks) trackMeta.set(t.url, { title: t.title, artist: t.artist ?? '' });
    wa.appendTracks(tracks);

    // Track identity for duration/title matching + sidebar now-playing.
    // Anchor the label to the BE map (Webamp's info.metaData gets
    // overwritten to "Unknown" for tag-less files) and fall back to it.
    wa.onTrackDidChange((info) => {
      const url = info?.url ?? '';
      const be = trackMeta.get(url);
      cur = {
        url,
        title: be?.title || info?.metaData.title || '',
        artist: be?.artist || info?.metaData.artist || '',
      };
      scheduleReport();
    });
    // Any store change (play/pause/seek/volume/position) → re-report,
    // keep the saved blob current, and apply a restored position once
    // playback actually starts (seekToTime on unloaded media no-ops).
    wa.__onStateChange(() => {
      scheduleReport();
      syncVolumeToService();
      if (pendingSeek > 0) {
        try {
          if (String(wa.getMediaStatus()).toUpperCase() === 'PLAYING') {
            const s = pendingSeek;
            pendingSeek = -1;
            wa.seekToTime(s);
          }
        } catch {
          pendingSeek = -1;
        }
      }
      maybePersist();
    });

    // Register with the control plane, seeding now-playing from track 0.
    const first = m.tracks[0];
    audio?.register({ title: first?.title ?? '', artist: first?.artist ?? '' });
    cur = { url: tracks[0]?.url ?? '', title: first?.title ?? '', artist: first?.artist ?? '' };

    // Restore the saved playback blob now that the playlist exists.
    // The player comes back paused on the saved track; the first play
    // resumes from the saved position via pendingSeek above.
    if (saved) {
      try {
        // Seed lastVolume so the restore's setVolume isn't mistaken for a
        // user change and pushed up as the new master — the service hands us
        // the real master via the register cmd instead.
        if (saved.vol != null) {
          lastVolume = saved.vol;
          wa.setVolume(saved.vol);
        }
        if (saved.shuffle && !wa.isShuffleEnabled()) wa.toggleShuffle();
        if (saved.repeat && !wa.isRepeatEnabled()) wa.toggleRepeat();
        const idx = m.tracks.findIndex((t) => t.url === saved!.track_url);
        if (idx >= 0) {
          wa.setCurrentTrack(idx); // does not autoplay while stopped
          const t = m.tracks[idx];
          cur = { url: t.url, title: t.title, artist: t.artist ?? '' };
          if ((saved.pos ?? 0) > 0) pendingSeek = saved.pos!;
        }
        lastPersisted = readPersistable(wa);
      } catch {
        /* restore is best-effort; a fresh player is the fallback */
      }
      saved = null;
    }
    scheduleReport();
  }

  const handleBE = (m: { kind?: string }) => {
    if (m?.kind === 'tracks_ok') void initWebamp(m as TracksOk);
  };
  // wash:state always fires before any queued wash:msg (api.ts
  // delivery order), so `saved` is populated before tracks_ok
  // triggers initWebamp's restore.
  const handleState = (s: PersistedWashamp | null) => {
    if (s?.track_url || s?.vol != null) saved = s;
  };

  const { send } = createAppBus(props, {
    onMsg: handleBE,
    onState: (s) => handleState(s as PersistedWashamp | null),
  });

  onMount(() => {
    // Producer bridge: transport/volume cmds drive webamp; reports read a
    // fresh snapshot. (audio.cmd is handled here, not in onMsg below.)
    audio = createAudioSource({
      instance: props.instance,
      host: props.host,
      snapshot: () => (webamp ? snapshot(webamp) : { title: '', status: 'stopped' }),
      onCmd: (action, value) => handleCmd(action, value),
    });
    // Capture-phase so we intercept Webamp's titlebar drag (see
    // onHostMouseDown). Harmless before Webamp renders — nothing matches
    // #title-bar yet.
    props.host.addEventListener('mousedown', onHostMouseDown, true);
    // A DPI change (e.g. dragging to a monitor at a different scale) fires
    // a window resize; re-snap when devicePixelRatio actually changed.
    window.addEventListener('resize', onWindowResize);
    onCleanup(() => {
      props.host.removeEventListener('mousedown', onHostMouseDown, true);
      window.removeEventListener('resize', onWindowResize);
      if (persistTimer) clearTimeout(persistTimer);
      setSmoothRendering(false);
    });
    send({ kind: 'tracks', id: 'm-tracks' });
  });

  onCleanup(() => {
    audio?.dispose();
    webamp?.dispose();
  });

  return (
    <div
      ref={container}
      data-testid="music-webamp"
      style={{ position: 'relative', width: '100%', height: '100%' }}
    />
  );
}

defineWashApp('wash-app-washamp', WashampApp, {
  // The window is chromeless and sized to Webamp's main window, so the
  // host needs no backdrop (transparent — no black margin) and must not
  // clip: Webamp's EQ/playlist sub-windows open beyond the main window's
  // box and should stay visible.
  style: 'display:block;width:100%;height:100%;overflow:visible;background:transparent;',
});

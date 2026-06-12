// wash-washamp FE — embeds Webamp (the JS classic-Winamp skin engine)
// inside a wash window, feeds it Case-1 tracks served by the BE over the
// ingress proxy (docs/AUDIO.md §1, §2), and bridges playback to the
// com.wash.audio control plane (§3): it reports now-playing/status/
// position up to the service (→ sidebar) and obeys transport/volume
// commands coming back down.

import { createAudioSource, defineWashApp, type AudioSource, type WashAppProps } from '@wash/ui';
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

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // The window is chromeless (apps/washamp/be/app.go sets
  // WindowHints.Chromeless): there is no wash titlebar, so Webamp's own
  // Winamp titlebar is the single titlebar and must drive the wash
  // window. windowID is the id the shell stamps on the host element at
  // mount (data-wash-window); we use it to call window.wash.{move,close,
  // minimize}Window from Webamp's native chrome.
  const windowID = Number(props.host.getAttribute('data-wash-window') || 0);
  const winInfo = () => window.wash.windows().find((w) => w.windowID === windowID);

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
    if (windowID) window.wash.resizeWindow(windowID, Math.round(mr.width), Math.round(mr.height));
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
      window.wash.minimizeWindow(windowID);
      return;
    }
    if (!t.closest('#title-bar')) return;
    if (t.closest('#clutter-bar, #option, #shade, #close')) return;
    const info = winInfo();
    if (!info || !windowID) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    window.wash.focusWindow(windowID);
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
      window.wash.moveWindow(windowID, nx, ny);
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
      window.wash.moveWindow(windowID, nx, ny);
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
        if (typeof value === 'number') wa.setVolume(Math.round(value * 100));
        break;
    }
    scheduleReport();
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
    wa.onClose(() => windowID && window.wash.closeWindow(windowID));
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
    // Any store change (play/pause/seek/volume/position) → re-report.
    wa.__onStateChange(scheduleReport);

    // Register with the control plane, seeding now-playing from track 0.
    const first = m.tracks[0];
    audio?.register({ title: first?.title ?? '', artist: first?.artist ?? '' });
    cur = { url: tracks[0]?.url ?? '', title: first?.title ?? '', artist: first?.artist ?? '' };
    scheduleReport();
  }

  onMount(() => {
    // Producer bridge: transport/volume cmds drive webamp; reports read a
    // fresh snapshot. (audio.cmd is handled here, not in onMsg below.)
    audio = createAudioSource({
      instance: props.instance,
      host: props.host,
      snapshot: () => (webamp ? snapshot(webamp) : { title: '', status: 'stopped' }),
      onCmd: (action, value) => handleCmd(action, value),
    });
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as { kind?: string };
      if (m?.kind === 'tracks_ok') void initWebamp(m as TracksOk);
    };
    props.host.addEventListener('wash:msg', onMsg);
    // Capture-phase so we intercept Webamp's titlebar drag (see
    // onHostMouseDown). Harmless before Webamp renders — nothing matches
    // #title-bar yet.
    props.host.addEventListener('mousedown', onHostMouseDown, true);
    // A DPI change (e.g. dragging to a monitor at a different scale) fires
    // a window resize; re-snap when devicePixelRatio actually changed.
    window.addEventListener('resize', onWindowResize);
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('mousedown', onHostMouseDown, true);
      window.removeEventListener('resize', onWindowResize);
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

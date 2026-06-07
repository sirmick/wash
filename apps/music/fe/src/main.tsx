// wash-music FE — embeds Webamp (the JS classic-Winamp skin engine)
// inside a wash window, feeds it Case-1 tracks served by the BE over the
// ingress proxy (docs/AUDIO.md §1, §2), and bridges playback to the
// com.wash.audio control plane (§3): it reports now-playing/status/
// position up to the service (→ sidebar) and obeys transport/volume
// commands coming back down.

import { defineWashApp, type WashAppProps } from '@wash/ui';
import { onCleanup, onMount } from 'solid-js';
import Webamp from 'webamp';

interface TracksOk {
  kind: 'tracks_ok';
  base: string;
  tracks: { file: string; title: string; artist: string }[];
}

interface AudioCmd {
  kind: 'audio.cmd';
  action: 'play' | 'pause' | 'next' | 'prev' | 'stop' | 'volume';
  value?: number; // 0..1 for volume
}

function MusicApp(props: WashAppProps) {
  let container!: HTMLDivElement;
  let webamp: Webamp | undefined;
  // Latest current-track identity, captured from onTrackDidChange (the
  // store doesn't carry per-track duration, so we match by url below).
  let cur = { url: '', title: '', artist: '' };

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

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
      if (t) {
        dur = (t as { duration?: number }).duration ?? 0;
        title = t.title ?? title;
        artist = t.artist ?? artist;
      }
    } catch {
      /* ignore — keep last-known fields */
    }
    return { title, artist, status, pos, dur };
  }

  // report is throttled: webamp's store ticks position roughly per second,
  // and we don't want to flood the bus on every store mutation.
  let reportTimer: number | null = null;
  function scheduleReport() {
    if (reportTimer != null) return;
    reportTimer = window.setTimeout(() => {
      reportTimer = null;
      if (!webamp) return;
      send({ kind: 'audio_report', ...snapshot(webamp) });
    }, 400);
  }

  function handleCmd(c: AudioCmd) {
    const wa = webamp;
    if (!wa) return;
    switch (c.action) {
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
        if (typeof c.value === 'number') wa.setVolume(Math.round(c.value * 100));
        break;
    }
    scheduleReport();
  }

  async function initWebamp(m: TracksOk) {
    if (webamp) return; // single Winamp per window
    const wa = new Webamp(); // default (built-in) classic skin
    webamp = wa;
    await wa.renderWhenReady(container);
    const tracks = m.tracks.map((t) => ({
      url: m.base + t.file,
      metaData: { title: t.title, artist: t.artist },
    }));
    wa.appendTracks(tracks);

    // Track identity for duration/title matching + sidebar now-playing.
    wa.onTrackDidChange((info) => {
      cur = {
        url: info?.url ?? '',
        title: info?.metaData.title ?? '',
        artist: info?.metaData.artist ?? '',
      };
      scheduleReport();
    });
    // Any store change (play/pause/seek/volume/position) → re-report.
    wa.__onStateChange(scheduleReport);

    // Register with the control plane, seeding now-playing from track 0.
    const first = m.tracks[0];
    send({ kind: 'audio_register', title: first?.title ?? '', artist: first?.artist ?? '' });
    cur = { url: tracks[0]?.url ?? '', title: first?.title ?? '', artist: first?.artist ?? '' };
    scheduleReport();
  }

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as { kind?: string };
      if (m?.kind === 'tracks_ok') void initWebamp(m as TracksOk);
      else if (m?.kind === 'audio.cmd') handleCmd(m as AudioCmd);
    };
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    send({ kind: 'tracks', id: 'm-tracks' });
  });

  onCleanup(() => {
    send({ kind: 'audio_unregister' });
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

defineWashApp('wash-app-music', MusicApp, {
  style: 'display:block;width:100%;height:100%;overflow:hidden;background:#000;',
});

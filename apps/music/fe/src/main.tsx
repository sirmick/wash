// wash-music FE — embeds Webamp (the JS classic-Winamp skin engine)
// inside a wash window and feeds it Case-1 tracks served by the BE over
// the ingress proxy (docs/AUDIO.md §1, §2).
//
// Flow: on mount we ask the BE for the track list; when it replies with
// the ingress base path we instantiate Webamp, render it into the host,
// and append the tracks. We appendTracks (not setTracksToPlay) so M1
// loads the playlist without auto-playing — playback starts on a user
// gesture, sidestepping browser autoplay policy.

import { defineWashApp, type WashAppProps } from '@wash/ui';
import { onCleanup, onMount } from 'solid-js';
import Webamp from 'webamp';

interface TracksOk {
  kind: 'tracks_ok';
  base: string;
  tracks: { file: string; title: string; artist: string }[];
}

function MusicApp(props: WashAppProps) {
  let container!: HTMLDivElement;
  let webamp: Webamp | undefined;

  async function initWebamp(m: TracksOk) {
    if (webamp) return; // single Winamp per window
    const wa = new Webamp(); // default (built-in) classic skin
    webamp = wa;
    await wa.renderWhenReady(container);
    wa.appendTracks(
      m.tracks.map((t) => ({
        url: m.base + t.file,
        metaData: { title: t.title, artist: t.artist },
      })),
    );
  }

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as { kind?: string };
      if (m?.kind === 'tracks_ok') void initWebamp(m as TracksOk);
    };
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    window.wash.sendAppMsg(props.instance, { kind: 'tracks', id: 'm-tracks' });
  });

  onCleanup(() => webamp?.dispose());

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

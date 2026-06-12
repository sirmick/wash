// audio-source — the producer (FE) side of the com.wash.audio contract
// (docs/AUDIO.md §3), shared by every wash media app (Washamp, the native
// Music/Radio players, a future video player). The register/report/
// unregister messages go to the app's OWN BE, which relays them cross-app
// to the service (internal/audiorelay); the service's transport/volume
// command comes back as a wash:msg `audio.cmd`.

type WashSend = { sendAppMsg(instanceID: string, data: unknown): void };
const wash = (): WashSend => (window as unknown as { wash: WashSend }).wash;

export interface AudioSnapshot {
  title: string;
  artist?: string;
  status: string; // playing | paused | stopped
  pos?: number;
  dur?: number;
}

export interface AudioSourceOptions {
  /** the app's own instance id (props.instance). */
  instance: string;
  /** the host element to listen on for wash:msg `audio.cmd`. */
  host: HTMLElement;
  /** read the producer's current playback state for a report. */
  snapshot: () => AudioSnapshot;
  /** a transport/volume command from the service/sidebar. */
  onCmd: (action: string, value?: number) => void;
  /** report-coalescing window, ms (default 400). */
  throttleMs?: number;
}

export interface AudioSource {
  /** announce now-playing — sends register + an immediate report. */
  register(meta: { title: string; artist?: string }): void;
  /** schedule a throttled report from snapshot(). */
  report(): void;
  /** unregister + detach the cmd listener. */
  dispose(): void;
}

/** Wire a producer's playback to com.wash.audio. The caller owns the
 *  actual `<audio>`/player; this is just the typed pipe + report throttle. */
export function createAudioSource(opts: AudioSourceOptions): AudioSource {
  const send = (msg: unknown) => wash().sendAppMsg(opts.instance, msg);

  const onMsg = (ev: Event) => {
    const m = (ev as CustomEvent).detail as
      | { kind?: string; action?: string; value?: number }
      | undefined;
    if (m?.kind === 'audio.cmd' && m.action) opts.onCmd(m.action, m.value);
  };
  opts.host.addEventListener('wash:msg', onMsg);

  let timer: number | null = null;
  const flush = () => {
    timer = null;
    const s = opts.snapshot();
    send({
      kind: 'audio_report',
      title: s.title,
      artist: s.artist ?? '',
      status: s.status,
      pos: s.pos ?? 0,
      dur: s.dur ?? 0,
    });
  };
  const report = () => {
    if (timer != null) return;
    timer = window.setTimeout(flush, opts.throttleMs ?? 400);
  };

  return {
    register(meta) {
      send({ kind: 'audio_register', title: meta.title, artist: meta.artist ?? '' });
      report();
    },
    report,
    dispose() {
      opts.host.removeEventListener('wash:msg', onMsg);
      if (timer != null) {
        window.clearTimeout(timer);
        timer = null;
      }
      send({ kind: 'audio_unregister' });
    },
  };
}

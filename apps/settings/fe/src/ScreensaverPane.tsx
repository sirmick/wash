// The Screensaver pane of Settings: on/off + timeout, a gallery of the
// savers (plus Random) with Preview, and the chosen saver's own options.
//
// Saves into desktop.json's `screensaver` block (see @wash/ui screensaver.ts
// for the shape). A saver's options are not known here: they come from its
// module (`options`), loaded when it is selected, so adding an option to an
// effect needs no Settings change.

import { For, Show, createResource, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import {
  Checkbox,
  RANDOM_SAVER,
  Row,
  SCREENSAVERS,
  Section,
  Select,
  SmallBtn,
  getSaver,
  loadSaver,
  optionValues,
  resolveSaver,
  saverThumbnailPath,
  showScreensaver,
  tokens,
} from '@wash/ui';
import type { SaverOption, ScreensaverConfig } from '@wash/ui';
import { Play, Shuffle } from 'lucide-solid';

const TIMEOUTS_MIN = [1, 2, 3, 5, 10, 15, 20, 30, 45, 60];

export const ScreensaverPane: Component<{
  value: ScreensaverConfig;
  onChange: (v: ScreensaverConfig) => void;
}> = (props) => {
  const set = (patch: Partial<ScreensaverConfig>) => props.onChange({ ...props.value, ...patch });
  const setOption = (id: string, key: string, v: unknown) =>
    set({ options: { ...props.value.options, [id]: { ...props.value.options[id], [key]: v } } });

  // The selected saver's option specs, from its module. Random and Blank
  // have none.
  const [specs] = createResource(
    () => props.value.saver,
    async (id): Promise<SaverOption[]> => (getSaver(id)?.module ? (await loadSaver(id)).options : []),
  );

  // A timeout set by hand (or by a test) that isn't one of the choices still
  // shows as itself.
  const timeoutChoices = (): [string, string][] => {
    const cur = props.value.timeout_s;
    const list = TIMEOUTS_MIN.map((m): [string, string] => [String(m * 60), m === 1 ? '1 minute' : `${m} minutes`]);
    if (!list.some(([v]) => v === String(cur))) list.unshift([String(cur), `${cur} seconds`]);
    return list;
  };

  const preview = () => {
    const id = resolveSaver(props.value.saver);
    showScreensaver(id, props.value.options[id]);
  };

  const optionsTitle = () => `${getSaver(props.value.saver)?.name ?? ''} options`;

  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="Start">
        <Row label="After">
          <div style={{ display: 'flex', gap: '12px', 'align-items': 'center' }}>
            <Checkbox
              checked={props.value.enabled}
              onChange={(v) => set({ enabled: v })}
              label="on"
              data-testid="screensaver-enabled"
            />
            <Select
              value={String(props.value.timeout_s)}
              options={timeoutChoices()}
              onChange={(v) => set({ timeout_s: Number(v) })}
              disabled={!props.value.enabled}
              data-testid="screensaver-timeout"
            />
            <span style={{ color: tokens.fgDim, font: tokens.type.textSm }}>of no input in this tab</span>
          </div>
        </Row>
      </Section>

      <Section title="Screensaver">
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
          <div style={galleryStyle}>
            <SaverCard
              id={RANDOM_SAVER}
              name="Random"
              active={props.value.saver === RANDOM_SAVER}
              onSelect={() => set({ saver: RANDOM_SAVER })}
            />
            <For each={SCREENSAVERS}>
              {(s) => (
                <SaverCard id={s.id} name={s.name} thumb={s.module} active={props.value.saver === s.id} onSelect={() => set({ saver: s.id })} />
              )}
            </For>
          </div>
          <div>
            <SmallBtn onClick={preview} data-testid="screensaver-preview">
              <Play size={14} /> Preview
            </SmallBtn>
          </div>
        </div>
      </Section>

      <Show when={(specs() ?? []).length > 0}>
        <Section title={optionsTitle()}>
          <div style={{ display: 'flex', 'flex-direction': 'column', gap: '8px' }} data-testid="screensaver-options">
            <For each={specs()}>
              {(o) => {
                const id = props.value.saver;
                const val = () => optionValues([o], props.value.options[id])[o.key];
                return (
                  <Row label={o.label}>
                    <OptionControl spec={o} value={val()} onChange={(v) => setOption(id, o.key, v)} />
                  </Row>
                );
              }}
            </For>
          </div>
        </Section>
      </Show>
      <Show when={specs.error}>
        <span style={{ color: tokens.fgDim, font: tokens.type.textSm }}>could not load this screensaver's options</span>
      </Show>
    </div>
  );
};

const OptionControl: Component<{ spec: SaverOption; value: unknown; onChange: (v: unknown) => void }> = (props) => {
  const s = props.spec;
  if (s.kind === 'select') {
    return <Select value={String(props.value)} options={s.choices} onChange={props.onChange} data-testid={`screensaver-opt-${s.key}`} />;
  }
  if (s.kind === 'toggle') {
    return <Checkbox checked={!!props.value} onChange={props.onChange} data-testid={`screensaver-opt-${s.key}`} />;
  }
  return (
    <div style={{ display: 'flex', gap: '8px', 'align-items': 'center' }}>
      <input
        type="range"
        min={s.min}
        max={s.max}
        step={s.step ?? (s.max - s.min) / 20}
        value={Number(props.value)}
        onInput={(e) => props.onChange(Number(e.currentTarget.value))}
        data-testid={`screensaver-opt-${s.key}`}
        style={{ width: '180px', 'accent-color': tokens.accentBlue }}
      />
      <span style={{ color: tokens.fgDim, font: tokens.type.monoSm, 'min-width': '3em' }}>{Number(props.value).toFixed(2)}</span>
    </div>
  );
};

// SaverCard: a thumbnail (fetched over the asset channel like the theme
// cards) and the name. Random gets an icon; Blank is a black tile.
const SaverCard: Component<{ id: string; name: string; thumb?: boolean; active: boolean; onSelect: () => void }> = (props) => {
  const [url, setUrl] = createSignal('');
  onMount(() => {
    if (!props.thumb) return;
    let objURL = '';
    let cancelled = false;
    window.wash
      .fetchAsset(saverThumbnailPath(props.id))
      .then((a) => {
        if (cancelled) return;
        objURL = URL.createObjectURL(new Blob([a.bytes], { type: a.mime || 'image/jpeg' }));
        setUrl(objURL);
      })
      .catch(() => {/* plain tile */});
    onCleanup(() => {
      cancelled = true;
      if (objURL) URL.revokeObjectURL(objURL);
    });
  });
  return (
    <button
      data-wash-hit
      type="button"
      onClick={props.onSelect}
      data-testid={`screensaver-card-${props.id}`}
      data-active={props.active}
      title={props.name}
      style={{
        display: 'flex',
        'flex-direction': 'column',
        gap: '6px',
        padding: '6px',
        background: props.active ? tokens.bgRowSelected : tokens.bgInset,
        border: `2px solid ${props.active ? tokens.accentBlue : tokens.borderMenu}`,
        'border-radius': `${tokens.radiusLg}`,
        cursor: 'pointer',
        width: '160px',
      }}
    >
      <div
        style={{
          width: '100%',
          height: '90px',
          display: 'flex',
          'align-items': 'center',
          'justify-content': 'center',
          color: tokens.fgDim,
          background: url() ? `#000 url("${url()}") center/cover no-repeat` : '#000',
          'border-radius': `${tokens.radiusMd}`,
        }}
      >
        <Show when={props.id === RANDOM_SAVER}>
          <Shuffle size={28} />
        </Show>
      </div>
      <span style={{ color: tokens.fg, font: tokens.type.textMd, 'text-align': 'left' }}>{props.name}</span>
    </button>
  );
};

const galleryStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-wrap': 'wrap',
  gap: '12px',
};

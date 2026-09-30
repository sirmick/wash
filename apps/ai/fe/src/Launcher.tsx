// The new-session form: catalog, model and folder; the launch's permissions;
// and, under Advanced, the adapter's own settings (effort, fast mode, …).
//
// A catalog (agentd's catalogs.go) is where a model comes from: an adapter's
// own list ("Anthropic": whatever Claude Code reports) or a curated set of
// three slots ("Anthropic pro": frontier, coding, small). Picking one and a
// model is the question a person actually has, and the same two questions a
// workspace member answers with "model":"coding". The form renders what
// agentd publishes and owns no launch logic: Start sends {catalog, model?,
// configs?, mode?, yolo?, cwd} and agentd resolves it.
//
// Permissions are a property of the launch, not of the catalog, and they are
// REMEMBERED: the Permissions row shows agents.json's launch default, a
// change to it writes that default (onLaunch), and Start sends what the row
// shows. A change made on a running session is that session's alone.

import { For, Show, createMemo, type Component, type JSX } from 'solid-js';
import { Button, Checkbox, Input, Select, tokens, type agentproto } from '@wash/ui';

export interface LaunchForm {
  catalog: string;
  /** a slot of a curated catalog, a model id, or '' for the default */
  model: string;
  /** Advanced: the adapter's own settings, by option id */
  configs: Record<string, string>;
  cwd: string;
}

export const emptyForm = (): LaunchForm => ({ catalog: '', model: '', configs: {}, cwd: '' });

export const SLOTS = ['frontier', 'coding', 'small'] as const;

export function slotLabel(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The slot a form's model names, if the catalog is curated and it does. */
export function chosenSlot(f: LaunchForm, catalogs: agentproto.CatalogView[]): agentproto.SlotView | undefined {
  const c = catalogs.find((x) => x.id === f.catalog);
  if (!c?.slots?.length) return undefined;
  return c.slots.find((s) => s.slot === (f.model || 'frontier'));
}

/** The adapter a start would run: the catalog's, or the chosen slot's. */
export function launchAdapter(f: LaunchForm, catalogs: agentproto.CatalogView[]): string {
  const c = catalogs.find((x) => x.id === f.catalog);
  if (!c) return '';
  if (!c.slots?.length) return c.adapter ?? '';
  return (chosenSlot(f, catalogs) ?? c.slots.find((s) => s.slot === 'frontier'))?.adapter ?? '';
}

/** What Start sends agentd: empty choices are left out, so agentd sees only
 *  what the person actually chose. The permissions are the remembered
 *  default for the adapter that will run. The launcher lives in the manager,
 *  which hands each session to a window of its own (open). */
export function startMessage(f: LaunchForm, catalogs: agentproto.CatalogView[], launch: agentproto.LaunchPrefs): agentproto.AgentStart {
  const msg: agentproto.AgentStart = { kind: 'agent_start', cwd: f.cwd, open: true };
  if (f.catalog) msg.catalog = f.catalog;
  if (f.model) msg.model = f.model;
  const configs = Object.fromEntries(Object.entries(f.configs).filter(([, v]) => v));
  if (Object.keys(configs).length) msg.configs = configs;
  const mode = launch.mode?.[launchAdapter(f, catalogs)];
  if (mode) msg.mode = mode;
  if (launch.yolo) msg.yolo = true;
  return msg;
}

/** Whether the form can start anything, and if not, why. */
export function launchBlocker(f: LaunchForm, catalogs: agentproto.CatalogView[]): string {
  const c = catalogs.find((x) => x.id === f.catalog);
  if (!c) return 'Choose a catalog.';
  if (!c.available) return c.note ?? `${c.name} cannot start here`;
  const slot = chosenSlot(f, catalogs);
  if (slot && !slot.available) return slot.note ?? `${c.name} cannot start here`;
  return '';
}

/** The values an adapter last offered for one setting category, if it
 *  has ever run here. */
export function optionValues(options: agentproto.AdapterOptions[], adapter: string, category: string): agentproto.ConfigValue[] {
  const cfg = options.find((o) => o.adapter === adapter)?.configs?.find((c) => c.category === category);
  return cfg?.values ?? [];
}

/** The adapter's settings Advanced offers: everything it reported except
 *  the model (the Model select) and the mode (the Permissions row). */
export function advancedOptions(options: agentproto.AdapterOptions[], adapter: string): agentproto.Config[] {
  return (options.find((o) => o.adapter === adapter)?.configs ?? []).filter((c) => c.category !== 'model' && c.category !== 'mode');
}

export const Launcher: Component<{
  catalogs: agentproto.CatalogView[];
  adapters: agentproto.Adapter[];
  adapterOptions: agentproto.AdapterOptions[];
  launch: agentproto.LaunchPrefs;
  onLaunch: (prefs: agentproto.LaunchPrefs) => void;
  form: LaunchForm;
  onForm: (patch: Partial<LaunchForm>) => void;
  onStart: () => void;
  onPickFolder: () => void;
  starting: boolean;
  error: string;
}> = (props) => {
  const catalog = createMemo(() => props.catalogs.find((c) => c.id === props.form.catalog));
  const curated = () => !!catalog()?.slots?.length;
  const slot = createMemo(() => chosenSlot(props.form, props.catalogs));
  const adapterName = (id: string) => props.adapters.find((a) => a.id === id)?.name ?? id;
  const blocker = createMemo(() => launchBlocker(props.form, props.catalogs));
  const adapter = createMemo(() => launchAdapter(props.form, props.catalogs));
  const seen = createMemo(() => props.adapterOptions.find((o) => o.adapter === adapter()));
  const models = createMemo(() => optionValues(props.adapterOptions, adapter(), 'model'));
  const advanced = createMemo(() => advancedOptions(props.adapterOptions, adapter()));
  const mode = () => props.launch.mode?.[adapter()] ?? '';
  const permissionsSummary = () => {
    const m = mode();
    const name = m ? (seen()?.modes?.find((x) => x.id === m)?.name ?? m) : adapter() ? `${adapterName(adapter())}'s default` : '';
    return [name, props.launch.yolo ? 'auto-approve on' : ''].filter(Boolean).join(' · ');
  };

  // The Model select: a curated catalog's slots first, then any other model
  // its adapter reported; an auto catalog lists what its adapter reported,
  // or only its default until it has run once.
  const modelOptions = createMemo<[string, string][]>(() => {
    const c = catalog();
    if (!c) return [['', 'Choose a catalog first']];
    const out: [string, string][] = [];
    if (c.slots?.length) {
      for (const s of c.slots) out.push([s.slot, `${slotLabel(s.slot)} — ${s.model || 'default model'}${s.effort ? ` (${s.effort})` : ''}`]);
    } else {
      out.push(['', `${adapterName(c.adapter ?? '')}'s default`]);
    }
    const listed = new Set(c.slots?.map((s) => s.model) ?? []);
    for (const m of models()) if (!listed.has(m.value)) out.push([m.value, m.name || m.value]);
    return out;
  });

  // One line saying what will run, settings included, so nothing is a
  // surprise at start.
  const summary = () => {
    const c = catalog();
    if (!c) return '';
    const parts = [adapterName(adapter())];
    const s = slot();
    const model = s ? s.model : props.form.model;
    parts.push(model || 'default model');
    const effortID = advanced().find((o) => o.category === 'thought_level')?.id;
    const effort = (effortID && props.form.configs[effortID]) || s?.effort;
    if (effort) parts.push(`effort ${effort}`);
    for (const o of advanced()) {
      const v = props.form.configs[o.id];
      if (v && o.category !== 'thought_level') parts.push(`${o.name.toLowerCase()} ${v}`);
    }
    const connection = s?.connection ?? c.connection;
    if (connection) parts.push(`via ${connection.split('@')[1] ?? connection}`);
    return parts.join(' · ');
  };

  const start = () => {
    if (!props.starting && !blocker()) props.onStart();
  };

  const setMode = (v: string) => {
    const next = { ...(props.launch.mode ?? {}) };
    if (v) next[adapter()] = v;
    else delete next[adapter()];
    props.onLaunch({ ...props.launch, mode: next });
  };
  const setConfig = (id: string, v: string) => {
    const next = { ...props.form.configs };
    if (v) next[id] = v;
    else delete next[id];
    props.onForm({ configs: next });
  };

  return (
    <div data-testid="ai-launcher" style={{ padding: `${tokens.spaceMd}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px` }}>
      {/* Compact on purpose: this pane shares its column with History, and
          every row here is a row History loses at the default window
          size. */}
      <div style={fieldStyle}>
        <div style={{ display: 'grid', 'grid-template-columns': 'minmax(0, 2fr) minmax(0, 3fr)', gap: `${tokens.spaceSm}px` }}>
          <label style={fieldStyle}>
            <span style={labelStyle}>catalog</span>
            <Select
              value={props.form.catalog}
              // Another catalog's model names mean nothing to this one.
              onChange={(v) => props.onForm({ catalog: v, model: '', configs: {} })}
              data-testid="ai-catalog-select"
              options={[
                ['', 'Choose…'],
                ...props.catalogs.map((c) => [c.id, c.available ? c.name : `${c.name} — ${c.note ?? 'unavailable'}`, !c.available] as [string, string, boolean]),
              ]}
            />
          </label>
          <label style={fieldStyle}>
            <span style={labelStyle}>model</span>
            <Select
              value={curated() ? props.form.model || 'frontier' : props.form.model}
              onChange={(v) => props.onForm({ model: v })}
              data-testid="ai-model-select"
              disabled={!catalog()}
              options={modelOptions()}
            />
          </label>
        </div>
        <Show when={summary()}>
          <span data-testid="ai-model-summary" style={{ font: tokens.type.monoSm, color: tokens.fgMuted }}>{summary()}</span>
        </Show>
      </div>

      <div style={fieldStyle}>
        <span style={labelStyle}>folder</span>
        <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'stretch' }}>
          <Input
            data-testid="ai-folder-input"
            spellcheck={false}
            placeholder="Home"
            value={props.form.cwd}
            title={props.form.cwd || 'Home'}
            onInput={(e: InputEvent) => props.onForm({ cwd: (e.currentTarget as HTMLInputElement).value })}
            onKeyDown={(e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); start(); } }}
            style={{ font: tokens.type.monoMd, flex: 1, 'min-width': 0 }}
          />
          <Button onClick={() => props.onPickFolder()}>Choose…</Button>
        </div>
      </div>

      {/* Permissions: the adapter's own approval preset, by the names it
          reported the last time it ran here, and wash's auto-approval.
          Both are the REMEMBERED default — changing one here changes what
          every later start begins with, which is the point: this is the
          row that was being re-set on every session. Neither is
          enforcement: a "read-only" preset on Codex still asks. */}
      {/* Collapsed like Advanced: remembered, so rarely changed per start. */}
      <details data-testid="ai-permissions" title="Remembered for every new session. A change made inside a running session is that session's only.">
        {/* Collapsed must not hide a yolo launch: the summary says what
            the next start gets. */}
        <summary data-wash-hit style={labelStyle}>
          permissions
          <span data-testid="ai-permissions-summary" style={{ color: props.launch.yolo ? tokens.sevWarn : tokens.fgMuted, 'margin-left': `${tokens.spaceSm}px` }}>
            {permissionsSummary()}
          </span>
        </summary>
        <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'align-items': 'center', 'flex-wrap': 'wrap', 'margin-top': `${tokens.spaceSm}px` }}>
          <Show
            when={adapter() && seen()?.modes?.length}
            fallback={
              <Select
                value=""
                disabled
                onChange={() => {}}
                data-testid="ai-mode-select"
                options={[['', adapter() ? `${adapterName(adapter())}'s default (its presets show after it has run once)` : 'Choose a catalog first']]}
              />
            }
          >
            <Select
              value={mode()}
              onChange={setMode}
              data-testid="ai-mode-select"
              options={[
                ['', `${adapterName(adapter())}'s default`],
                ...(seen()?.modes ?? []).map((m) => [m.id, m.name] as [string, string]),
              ]}
            />
          </Show>
          <Checkbox
            checked={!!props.launch.yolo}
            onChange={(v) => props.onLaunch({ ...props.launch, yolo: v })}
            data-testid="ai-yolo"
            label="Auto-approve everything (yolo)"
          />
        </div>
      </details>

      {/* The adapter's own settings, by the names and values it reported:
          effort, fast mode, whatever else it has. A one-off for this start,
          on top of the catalog's; the catalog itself is edited on its tab. */}
      <details data-testid="ai-advanced" open={Object.keys(props.form.configs).length > 0}>
        <summary data-wash-hit style={labelStyle}>advanced</summary>
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px`, 'margin-top': `${tokens.spaceSm}px` }}>
          <Show
            when={advanced().length > 0}
            fallback={
              <span data-testid="ai-advanced-empty" style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
                {adapter() ? `${adapterName(adapter())}'s settings show here after it has run once.` : 'Choose a catalog first.'}
              </span>
            }
          >
            <div style={{ display: 'grid', 'grid-template-columns': 'repeat(auto-fill, minmax(18ch, 1fr))', gap: `${tokens.spaceMd}px` }}>
              <For each={advanced()}>{(o) => (
                <label style={fieldStyle}>
                  <span style={labelStyle}>{o.name}</span>
                  <Select
                    value={props.form.configs[o.id] ?? ''}
                    onChange={(v) => setConfig(o.id, v)}
                    data-testid={`ai-config-${o.id}`}
                    options={[
                      ['', o.category === 'thought_level' && slot()?.effort ? `The slot's (${slot()!.effort})` : 'default'],
                      ...(o.values ?? []).map((v) => [v.value, v.name || v.value] as [string, string]),
                    ]}
                  />
                </label>
              )}</For>
            </div>
          </Show>
        </div>
      </details>

      <Show when={props.error}>
        <div data-testid="ai-start-error" style={errorStyle}>{props.error}</div>
      </Show>

      <Show when={blocker() && props.form.catalog}>
        <div data-testid="ai-start-blocker" style={{ font: tokens.type.textSm, color: tokens.fgMuted }}>{blocker()}</div>
      </Show>
      {/* The default prompt is edited on the Setup tab, with the rest of
          the machine's configuration. */}
      <div style={{ display: 'flex', 'align-items': 'center', 'justify-content': 'flex-end' }}>
        <Button variant="primary" data-testid="ai-start" disabled={props.starting || !!blocker()} onClick={start}>
          {props.starting ? 'Starting…' : 'Start session'}
        </Button>
      </div>
    </div>
  );
};

export const fieldStyle: JSX.CSSProperties = { display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` };

export const labelStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  'letter-spacing': '0.09em',
  'text-transform': 'uppercase',
  color: tokens.fgDim,
};

export const errorStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  color: tokens.fgDanger,
  background: tokens.bgDanger,
  border: `1px solid ${tokens.borderDanger}`,
  'border-radius': tokens.radiusMd,
  padding: `${tokens.spaceMd}px`,
};

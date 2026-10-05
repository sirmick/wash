// The catalogs section of the Setup tab: the machine's catalogs, editable.
// The keys their connections need sit above it (Connections.tsx).
//
// A catalog is an adapter's own model list (adapter and connection; the
// models come from what the adapter reported) or three slots (frontier,
// coding, small), each a model on an adapter with an effort — and nothing
// else. Permissions are not here on purpose: they belong to a launch (the
// launcher's Permissions row, a member's flags). The pane edits a draft per
// catalog and sends the WHOLE catalog on Save (agent_set_catalog), so
// agents.json says what this screen showed. A built-in catalog with an
// override offers Reset; one of the user's own offers Delete; both are
// agent_delete_catalog.
//
// Model and effort choices come from what each adapter last reported
// (State.AdapterOptions). An adapter that has never run here gets free text
// and says so; the launch path still checks the value against what the
// session offers, naming the alternatives, so a typo cannot start a session
// on a model nobody chose.

import { For, Show, createMemo, createSignal, type Component, type JSX } from 'solid-js';
import { Button, Input, Select, tokens, type agentproto } from '@wash/ui';
import { SLOTS, fieldStyle, labelStyle, optionValues, slotLabel } from './Launcher.tsx';

/** DefaultRow is what "start an agent" means when nobody says otherwise:
 *  the catalog and model the Editor's "new agent here", the Places agent
 *  icon and `wash ai <dir>` all resolve against (docs/PLACES.md §4.5).
 *
 *  It lives here rather than in the launcher because it is configuration,
 *  not a choice about the session being started — the launcher's own
 *  selects preselect FROM it. Unset is legal and means the old behaviour:
 *  the catalog used last, else the first adapter that can run.
 */
const DefaultRow: Component<{
  catalogs: agentproto.CatalogView[];
  adapterOptions: agentproto.AdapterOptions[];
  launch: agentproto.LaunchPrefs;
  onDefault: (catalog: string, model: string) => void;
}> = (props) => {
  const chosen = createMemo(() => props.catalogs.find((c) => c.id === props.launch.catalog));
  // Only catalogs that can actually start: a default nobody can run is a
  // trap, and agentd rejects it on save anyway.
  // A stored default that can no longer start (its key cleared since) stays
  // listed, disabled and labelled. Dropping it would make the select show
  // "No default" while agentd still resolves every start against it — the
  // screen would say one thing and the launch do another.
  const catalogOptions = createMemo<[string, string, boolean?][]>(() => {
    const out: [string, string, boolean?][] = [['', 'No default']];
    for (const c of props.catalogs) {
      if (c.available) out.push([c.id, c.name || c.id]);
      else if (c.id === props.launch.catalog) out.push([c.id, `${c.name || c.id} (cannot start here)`, true]);
    }
    return out;
  });
  const modelOptions = createMemo<[string, string][]>(() => {
    const c = chosen();
    if (!c) return [];
    const out: [string, string][] = [];
    if (c.slots?.length) {
      for (const s of c.slots) out.push([s.slot, `${slotLabel(s.slot)} — ${s.model || 'default model'}`]);
    } else {
      out.push(['', "the adapter's default"]);
    }
    // A curated catalog carries no adapter of its own — its slots do — so
    // the models on offer are the FRONTIER slot's adapter's, matching how a
    // launch resolves a bare model id against a curated catalog.
    const adapter = c.slots?.length ? (c.slots.find((s) => s.slot === 'frontier') ?? c.slots[0]).adapter : (c.adapter ?? '');
    const listed = new Set(c.slots?.map((s) => s.model) ?? []);
    for (const m of optionValues(props.adapterOptions, adapter, 'model')) {
      if (!listed.has(m.value)) out.push([m.value, m.name || m.value]);
    }
    return out;
  });
  return (
    <div data-testid="ai-default-catalog" style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` }}>
      <div style={fieldStyle}>
        <span style={labelStyle}>default for new agents</span>
        <span style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
          What starts when a session is opened from a folder — the Editor, the Files and Terminal agent icons, and <code>wash ai &lt;dir&gt;</code>. Unset uses whichever catalog you used last.
        </span>
      </div>
      <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'center', 'flex-wrap': 'wrap' }}>
        <Select
          data-testid="ai-default-catalog-select"
          value={props.launch.catalog ?? ''}
          options={catalogOptions()}
          onChange={(v) => props.onDefault(v, '')}
        />
        <Show when={chosen()}>
          <Select
            data-testid="ai-default-model-select"
            // A curated catalog offers no '' option; an unset model there is
            // its default slot, as the Launcher shows it.
            value={chosen()?.slots?.length ? props.launch.model || 'frontier' : (props.launch.model ?? '')}
            options={modelOptions()}
            onChange={(v) => props.onDefault(props.launch.catalog ?? '', v)}
          />
        </Show>
      </div>
    </div>
  );
};

/** The outcome of the last save or delete of one catalog. */
export interface CatalogResult {
  ok?: boolean;
  detail?: string;
}

type SlotDraft = { adapter: string; connection: string; model: string; effort: string };
type CatalogDraft = { name: string; adapter: string; connection: string; slots: Record<string, SlotDraft> };

const emptySlot = (): SlotDraft => ({ adapter: '', connection: '', model: '', effort: '' });

/** A catalog as agentd publishes it, as an editable draft. A missing slot
 *  (an invalid catalog from agents.json) starts empty rather than absent. */
export function draftOf(c: agentproto.CatalogView): CatalogDraft {
  const slots: Record<string, SlotDraft> = {};
  if (c.slots?.length) {
    for (const t of SLOTS) {
      const v = c.slots.find((x) => x.slot === t);
      slots[t] = { adapter: v?.adapter ?? '', connection: v?.connection ?? '', model: v?.model ?? '', effort: v?.effort ?? '' };
    }
  }
  return { name: c.name, adapter: c.adapter ?? '', connection: c.connection ?? '', slots };
}

export const isAuto = (d: CatalogDraft) => Object.keys(d.slots).length === 0;

/** What Save sends: the draft as a CatalogSpec, empty fields left out. */
export function specOf(d: CatalogDraft): agentproto.CatalogSpec {
  const spec: agentproto.CatalogSpec = { name: d.name.trim() };
  if (isAuto(d)) {
    spec.adapter = d.adapter;
    if (d.connection) spec.connection = d.connection;
    return spec;
  }
  spec.slots = {};
  for (const t of SLOTS) {
    const v = d.slots[t];
    const s: agentproto.SlotSpec = { adapter: v.adapter };
    if (v.connection) s.connection = v.connection;
    if (v.model) s.model = v.model;
    if (v.effort) s.effort = v.effort;
    spec.slots[t] = s;
  }
  return spec;
}

export function sameDraft(a: CatalogDraft, b: CatalogDraft): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** Why a draft cannot be saved, or ''. agentd checks again; this is so the
 *  button says why before the round trip. */
export function draftBlocker(d: CatalogDraft): string {
  if (!d.name.trim()) return 'Give the catalog a name.';
  if (isAuto(d)) return d.adapter ? '' : 'Choose an adapter.';
  for (const t of SLOTS) {
    if (!d.slots[t].adapter) return `Choose an adapter for the ${t} slot.`;
  }
  return '';
}

export const CatalogPane: Component<{
  catalogs: agentproto.CatalogView[];
  adapters: agentproto.Adapter[];
  connections: agentproto.ConnectionView[];
  adapterOptions: agentproto.AdapterOptions[];
  results: Record<string, CatalogResult>;
  launch: agentproto.LaunchPrefs;
  onSave: (id: string, catalog: agentproto.CatalogSpec) => void;
  onDelete: (id: string) => void;
  onDefault: (catalog: string, model: string) => void;
}> = (props) => {
  // Drafts by catalog id. A catalog the person has not touched follows the
  // roster; one being edited keeps its edits until Save or Discard, so a
  // roster push mid-edit (another window saved a key) does not eat them.
  const [edits, setEdits] = createSignal<Record<string, CatalogDraft>>({});
  const draft = (c: agentproto.CatalogView) => edits()[c.id] ?? draftOf(c);
  const dirty = (c: agentproto.CatalogView) => !!edits()[c.id] && !sameDraft(edits()[c.id], draftOf(c));
  const patch = (c: agentproto.CatalogView, d: CatalogDraft) => setEdits((all) => ({ ...all, [c.id]: d }));
  const discard = (id: string) => setEdits((all) => { const next = { ...all }; delete next[id]; return next; });
  // Keyed by id, not by the roster's objects: every roster push (any
  // session's activity) carries a fresh catalogs array, and a card rebuilt
  // under a person's cursor loses their focus mid-word.
  const ids = createMemo(() => props.catalogs.map((c) => c.id), [], { equals: (a, b) => a.join('\n') === b.join('\n') });

  // A new catalog, before it exists anywhere but here.
  const [adding, setAdding] = createSignal(false);
  const [newID, setNewID] = createSignal('');
  const curatedDraft = (): CatalogDraft => ({ name: '', adapter: '', connection: '', slots: Object.fromEntries(SLOTS.map((t) => [t, emptySlot()])) });
  const [newDraft, setNewDraft] = createSignal<CatalogDraft>(curatedDraft());
  const newIDBlocker = () => {
    const id = newID().trim();
    if (!id) return 'Give the catalog an id (letters, digits, - and _).';
    if (!/^[A-Za-z0-9_-]+$/.test(id)) return 'An id is letters, digits, - and _.';
    if (props.catalogs.some((c) => c.id === id)) return `There is already a catalog "${id}".`;
    return '';
  };

  const adapterName = (id: string) => props.adapters.find((a) => a.id === id)?.name ?? id;
  const adapterOptionsList = () => [
    ['', 'Adapter…'] as [string, string],
    ...props.adapters.map((a) => [a.id, a.available ? a.name : `${a.name} — ${a.note ?? 'not installed'}`] as [string, string]),
  ];
  const connectionOptions = (adapter: string) => [
    ['', 'direct'] as [string, string],
    ...props.connections.filter((c) => c.adapter === adapter).map((c) => [c.id, c.id.split('@')[1] ?? c.id] as [string, string]),
  ];

  const SlotRow: Component<{ catalogID: string; slot: string; value: SlotDraft; onChange: (v: SlotDraft) => void }> = (row) => {
    const models = createMemo(() => optionValues(props.adapterOptions, row.value.adapter, 'model'));
    const efforts = createMemo(() => optionValues(props.adapterOptions, row.value.adapter, 'thought_level'));
    const listID = `ai-catalog-models-${row.catalogID}-${row.slot}`;
    const set = (p: Partial<SlotDraft>) => row.onChange({ ...row.value, ...p });
    return (
      <div data-testid={`ai-catalog-slot-${row.catalogID}-${row.slot}`} style={{ display: 'grid', 'grid-template-columns': '8ch minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 2fr) minmax(0, 1fr)', gap: `${tokens.spaceSm}px`, 'align-items': 'center' }}>
        <span style={{ font: tokens.type.textMd, color: tokens.fgMuted, 'white-space': 'nowrap' }}>{slotLabel(row.slot)}</span>
        <Select
          value={row.value.adapter}
          data-testid={`ai-catalog-adapter-${row.catalogID}-${row.slot}`}
          // Another adapter's connection, model and effort mean nothing to
          // this one: the slot starts over on its defaults.
          onChange={(v) => row.onChange({ adapter: v, connection: '', model: '', effort: '' })}
          options={adapterOptionsList()}
        />
        <Select
          value={row.value.connection}
          data-testid={`ai-catalog-connection-${row.catalogID}-${row.slot}`}
          disabled={!row.value.adapter || connectionOptions(row.value.adapter).length === 1}
          onChange={(v) => set({ connection: v })}
          options={connectionOptions(row.value.adapter)}
        />
        <Input
          data-testid={`ai-catalog-model-${row.catalogID}-${row.slot}`}
          spellcheck={false}
          list={listID}
          disabled={!row.value.adapter}
          placeholder={models().length ? 'model (pick or type)' : 'model (its default; the list shows after it has run once)'}
          value={row.value.model}
          onInput={(e: InputEvent) => set({ model: (e.currentTarget as HTMLInputElement).value.trim() })}
          style={{ font: tokens.type.monoMd, 'min-width': 0 }}
        />
        <datalist id={listID}><For each={models()}>{(m) => <option value={m.value}>{m.name}</option>}</For></datalist>
        {/* A model the adapter did not list last time is not refused —
            a launch runs on the adapter's default and says so — but it is
            the one typo the Setup tab can catch before a workspace does:
            "sonnect" sat in a slot for days (Redoubt). */}
        <Show when={row.value.model && models().length > 0 && !models().some((m) => m.value === row.value.model)}>
          <div data-testid={`ai-catalog-model-note-${row.catalogID}-${row.slot}`} style={{ 'grid-column': '2 / -1', font: tokens.type.textSm, color: tokens.fgWarning }}>
            {row.value.model} is not in the list {adapterName(row.value.adapter)} last reported; a launch would run on its default model.
          </div>
        </Show>
        <Show
          when={efforts().length > 0}
          fallback={
            <Input
              data-testid={`ai-catalog-effort-${row.catalogID}-${row.slot}`}
              spellcheck={false}
              disabled={!row.value.adapter}
              placeholder="effort"
              value={row.value.effort}
              onInput={(e: InputEvent) => set({ effort: (e.currentTarget as HTMLInputElement).value.trim() })}
              style={{ font: tokens.type.monoMd, 'min-width': 0 }}
            />
          }
        >
          <Select
            value={row.value.effort}
            data-testid={`ai-catalog-effort-${row.catalogID}-${row.slot}`}
            onChange={(v) => set({ effort: v })}
            options={[['', 'default effort'], ...efforts().map((v) => [v.value, v.name] as [string, string])]}
          />
        </Show>
      </div>
    );
  };

  const CatalogCard: Component<{ id: string; view?: agentproto.CatalogView; draft: CatalogDraft; onDraft: (d: CatalogDraft) => void; actions: JSX.Element }> = (card) => (
    <section data-testid={`ai-catalog-${card.id}`} style={cardStyle}>
      <div style={{ display: 'flex', gap: `${tokens.spaceMd}px`, 'align-items': 'center' }}>
        <Input
          data-testid={`ai-catalog-name-${card.id}`}
          spellcheck={false}
          placeholder="Name"
          value={card.draft.name}
          onInput={(e: InputEvent) => card.onDraft({ ...card.draft, name: (e.currentTarget as HTMLInputElement).value })}
          style={{ flex: 1, 'min-width': 0 }}
        />
        <span style={{ font: tokens.type.monoSm, color: tokens.fgDim }}>{card.id}</span>
        <Show when={card.view?.builtin}>
          <span data-testid={`ai-catalog-origin-${card.id}`} style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
            {card.view?.overridden ? 'built in, changed here' : 'built in'}
          </span>
        </Show>
      </div>
      <Show when={card.view && !card.view.available}>
        <div data-testid={`ai-catalog-note-${card.id}`} style={{ font: tokens.type.textSm, color: tokens.fgWarning }}>{card.view!.note}</div>
      </Show>
      <Show
        when={!isAuto(card.draft)}
        fallback={
          <div data-testid={`ai-catalog-auto-${card.id}`} style={{ display: 'grid', 'grid-template-columns': '8ch minmax(0, 1fr) minmax(0, 1fr)', gap: `${tokens.spaceSm}px`, 'align-items': 'center' }}>
            <span style={{ font: tokens.type.textMd, color: tokens.fgMuted }}>Adapter</span>
            <Select
              value={card.draft.adapter}
              data-testid={`ai-catalog-adapter-${card.id}`}
              onChange={(v) => card.onDraft({ ...card.draft, adapter: v, connection: '' })}
              options={adapterOptionsList()}
            />
            <Select
              value={card.draft.connection}
              data-testid={`ai-catalog-connection-${card.id}`}
              disabled={!card.draft.adapter || connectionOptions(card.draft.adapter).length === 1}
              onChange={(v) => card.onDraft({ ...card.draft, connection: v })}
              options={connectionOptions(card.draft.adapter)}
            />
            <span />
            <span style={{ font: tokens.type.textSm, color: tokens.fgDim, 'grid-column': 'span 2' }}>
              Its models are whatever {card.draft.adapter ? adapterName(card.draft.adapter) : 'the adapter'} reports.
            </span>
          </div>
        }
      >
        <For each={SLOTS}>{(t) => (
          <SlotRow catalogID={card.id} slot={t} value={card.draft.slots[t]} onChange={(v) => card.onDraft({ ...card.draft, slots: { ...card.draft.slots, [t]: v } })} />
        )}</For>
      </Show>
      <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'center', 'flex-wrap': 'wrap' }}>
        {card.actions}
        <Show when={props.results[card.id]?.detail}>
          <span data-testid={`ai-catalog-result-${card.id}`} style={{ font: tokens.type.textSm, color: props.results[card.id]?.ok ? tokens.fgMuted : tokens.fgDanger }}>
            {props.results[card.id]?.detail}
          </span>
        </Show>
      </div>
    </section>
  );

  return (
    <div data-testid="ai-catalogs" style={{ padding: `${tokens.spaceMd}px`, display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceLg}px` }}>
      <div style={fieldStyle}>
        <span style={labelStyle}>catalogs</span>
        <span style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
          Where a model comes from: an adapter's own list, or three slots. What a session may do is set when it starts, not here.
        </span>
      </div>
      <DefaultRow catalogs={props.catalogs} adapterOptions={props.adapterOptions} launch={props.launch} onDefault={props.onDefault} />
      <For each={ids()}>{(id) => {
        const c = () => props.catalogs.find((x) => x.id === id)!;
        return (
          <CatalogCard
            id={id}
            view={c()}
            draft={draft(c())}
            onDraft={(d) => patch(c(), d)}
            actions={
              <>
                <Button
                  variant="primary"
                  data-testid={`ai-catalog-save-${id}`}
                  disabled={!dirty(c()) || !!draftBlocker(draft(c()))}
                  title={draftBlocker(draft(c())) || undefined}
                  onClick={() => { props.onSave(id, specOf(draft(c()))); discard(id); }}
                >
                  Save
                </Button>
                <Show when={dirty(c())}>
                  <Button variant="ghost" data-testid={`ai-catalog-discard-${id}`} onClick={() => discard(id)}>Discard</Button>
                </Show>
                <Show when={c().builtin && c().overridden}>
                  <Button variant="ghost" data-testid={`ai-catalog-reset-${id}`} onClick={() => { discard(id); props.onDelete(id); }}>Reset to built-in</Button>
                </Show>
                <Show when={!c().builtin}>
                  <Button variant="danger" data-testid={`ai-catalog-delete-${id}`} onClick={() => { discard(id); props.onDelete(id); }}>Delete</Button>
                </Show>
              </>
            }
          />
        );
      }}</For>

      <Show
        when={adding()}
        fallback={<div><Button data-testid="ai-catalog-add" onClick={() => setAdding(true)}>Add catalog…</Button></div>}
      >
        <CatalogCard
          id={newID().trim() || 'new'}
          draft={newDraft()}
          onDraft={setNewDraft}
          actions={
            <>
              <label style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'center' }}>
                <span style={labelStyle}>id</span>
                <Input data-testid="ai-catalog-new-id" spellcheck={false} placeholder="my-catalog" value={newID()}
                  onInput={(e: InputEvent) => setNewID((e.currentTarget as HTMLInputElement).value)} style={{ font: tokens.type.monoMd, width: '16ch' }} />
              </label>
              <Select
                value={isAuto(newDraft()) ? 'auto' : 'slots'}
                data-testid="ai-catalog-new-kind"
                onChange={(v) => setNewDraft((d) => v === 'auto' ? { ...d, slots: {} } : { ...d, adapter: '', connection: '', slots: Object.fromEntries(SLOTS.map((t) => [t, emptySlot()])) })}
                options={[['slots', 'Three slots'], ['auto', "An adapter's own list"]]}
              />
              <Button
                variant="primary"
                data-testid="ai-catalog-new-save"
                disabled={!!newIDBlocker() || !!draftBlocker(newDraft())}
                title={newIDBlocker() || draftBlocker(newDraft()) || undefined}
                onClick={() => {
                  props.onSave(newID().trim(), specOf(newDraft()));
                  setAdding(false);
                  setNewID('');
                  setNewDraft(curatedDraft());
                }}
              >
                Save
              </Button>
              <Button variant="ghost" data-testid="ai-catalog-new-cancel" onClick={() => setAdding(false)}>Cancel</Button>
              <span style={{ font: tokens.type.textSm, color: tokens.fgDim }}>{newIDBlocker() || draftBlocker(newDraft())}</span>
            </>
          }
        />
      </Show>

      <span style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
        Catalogs are kept in agents.json; a model an adapter no longer offers fails the start and names the ones it does.
        {' '}Adapters seen here: {props.adapterOptions.length ? props.adapterOptions.map((o) => `${adapterName(o.adapter)} ${o.version ?? ''}`.trim()).join(', ') : 'none yet'}.
      </span>
    </div>
  );
};

const cardStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: `${tokens.spaceSm}px`,
  padding: `${tokens.spaceMd}px`,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusMd,
  background: tokens.bgInset,
};

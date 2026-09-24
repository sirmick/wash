// The Connections section of the new-session form: the keys connections
// need, such as an OpenRouter API key.
//
// A key is typed here and sent to agentd once, to be saved or tested. It is
// never sent back: the roster says only whether a key is set and its last
// four characters. agentd keeps it in keys.json beside agents.json, readable
// only by the owner.

import { For, Show, createSignal, type Component } from 'solid-js';
import { Button, Input, tokens, type agentproto } from '@wash/ui';
import { labelStyle } from './Launcher.tsx';

/** The last save or test outcome for one key. */
export interface KeyResult {
  ok?: boolean;
  detail?: string;
  busy?: boolean;
}

export const Connections: Component<{
  keys: agentproto.KeyView[];
  results: Record<string, KeyResult>;
  onSave: (id: string, value: string) => void;
  onTest: (id: string, value: string) => void;
}> = (props) => {
  const [drafts, setDrafts] = createSignal<Record<string, string>>({});
  const draft = (id: string) => drafts()[id] ?? '';
  const setDraft = (id: string, v: string) => setDrafts((d) => ({ ...d, [id]: v }));

  return (
    <Show when={props.keys.length > 0}>
      <section data-testid="ai-connections" style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceSm}px` }}>
        <span style={labelStyle}>connections</span>
        <For each={props.keys}>
          {(k) => {
            const result = () => props.results[k.id];
            const save = (value: string) => {
              props.onSave(k.id, value);
              setDraft(k.id, '');
            };
            return (
              <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` }}>
                <div style={{ display: 'flex', 'justify-content': 'space-between', font: tokens.type.textSm }}>
                  <span style={{ color: tokens.fg }}>{k.name}</span>
                  <span data-testid={`ai-key-status-${k.id}`} style={{ color: k.set ? tokens.fgMuted : tokens.fgWarning, font: tokens.type.monoSm }}>
                    {k.set ? `set · …${k.hint ?? ''}` : 'not set'}
                  </span>
                </div>
                <div style={{ display: 'flex', gap: `${tokens.spaceSm}px` }}>
                  <Input
                    type="password"
                    autocomplete="off"
                    spellcheck={false}
                    data-testid={`ai-key-input-${k.id}`}
                    placeholder={k.set ? 'Paste a new key to replace it' : 'Paste the key'}
                    value={draft(k.id)}
                    onInput={(e: InputEvent) => setDraft(k.id, (e.currentTarget as HTMLInputElement).value)}
                    onKeyDown={(e: KeyboardEvent) => { if (e.key === 'Enter' && draft(k.id).trim()) { e.preventDefault(); save(draft(k.id)); } }}
                    style={{ flex: 1, 'min-width': 0, font: tokens.type.monoMd }}
                  />
                  <Button data-testid={`ai-key-save-${k.id}`} disabled={!draft(k.id).trim()} onClick={() => save(draft(k.id))}>Save</Button>
                  <Show when={k.testable}>
                    {/* Tests what is typed, else what is stored. */}
                    <Button data-testid={`ai-key-test-${k.id}`} disabled={result()?.busy || (!draft(k.id).trim() && !k.set)}
                      onClick={() => props.onTest(k.id, draft(k.id).trim())}>
                      {result()?.busy ? 'Testing…' : 'Test'}
                    </Button>
                  </Show>
                  <Show when={k.set}>
                    <Button variant="ghost" data-testid={`ai-key-clear-${k.id}`} onClick={() => save('')}>Clear</Button>
                  </Show>
                </div>
                <Show when={result()?.detail}>
                  <span data-testid={`ai-key-result-${k.id}`} style={{ font: tokens.type.textSm, color: result()?.ok ? tokens.fgMuted : tokens.fgDanger }}>
                    {result()?.detail}
                  </span>
                </Show>
              </div>
            );
          }}
        </For>
        <span style={{ font: tokens.type.textSm, color: tokens.fgDim }}>
          Kept in keys.json beside agents.json, readable only by you. There is no keychain yet.
        </span>
      </section>
    </Show>
  );
};

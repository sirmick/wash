// The Agents window's Permissions tab: the approval policy agentd answers a
// hosted session's tool permission requests from (agentproto/policy.go,
// agents.json `enabled` / `default` / `ask_desktop` / `rules`).
//
// It covers the sessions wash runs, the ones started here. An agent run by
// hand in a terminal never asks wash anything, and the tab says so.
//
// The policy arrives with every roster push (State.Policy, re-read from disk
// each tick, so a rule "always allow" just added shows up). Editing works on
// a local draft that a push never overwrites mid-edit; the draft is saved
// whole with agent_set_policy, and agentd changes only those four fields of
// the file. When the file changes underneath with no edit in flight (a rule
// added from the sidebar, a hand edit), the tab follows it.
//
// The UI is deliberately blunt about what this is: a switch that lets a
// program act without asking you. It ships off, and it never offers "allow"
// as the default for an unmatched request.

import { Index, Show, createEffect, createSignal, onCleanup, untrack, type Component, type JSX } from 'solid-js';
import { Checkbox, Input, Row, Section, Select, SmallBtn, tokens, type agentproto } from '@wash/ui';

/** The last save's outcome. */
export interface PolicyResult {
  ok?: boolean;
  detail?: string;
}

const DECISIONS: [string, string][] = [
  ['ask', 'ask me'],
  ['allow', 'allow'],
  ['deny', 'deny'],
];

// The unmatched-request decision. "allow" is deliberately not offered (and
// agentd refuses it): a default-allow policy is indistinguishable from
// having no permission system at all.
const DEFAULTS: [string, string][] = [
  ['ask', 'ask me (recommended)'],
  ['deny', 'deny'],
];

/** How long typing settles before the draft is saved. */
export const POLICY_SAVE_DELAY_MS = 400;

const OFF: agentproto.PolicyView = { enabled: false, default: 'ask', ask_desktop: true };

// What goes up: rows still being typed (no tool yet) stay in the draft but
// are not sent, since agentd refuses an empty match.
const sendable = (p: agentproto.PolicyView): agentproto.PolicyView => ({
  ...p,
  rules: (p.rules ?? []).filter((r) => r.match.trim() !== '').map((r) => ({ ...r, cwd: r.cwd?.trim() || undefined })),
});

export const Permissions: Component<{
  policy: agentproto.PolicyView | undefined;
  result?: PolicyResult;
  onSave: (p: agentproto.PolicyView) => void;
}> = (props) => {
  const [draft, setDraft] = createSignal<agentproto.PolicyView | null>(null);
  const view = (): agentproto.PolicyView => draft() ?? props.policy ?? OFF;
  const rules = (): agentproto.PolicyRule[] => view().rules ?? [];

  // `known` is what the draft was started from or last sent, and `before`
  // the value before that send: pushes equal to either are this tab's own
  // state (or its save not yet echoed), not a change made elsewhere.
  let timer = 0;
  let known = '';
  let before = '';
  const key = (p: agentproto.PolicyView | undefined) => JSON.stringify(sendable(p ?? OFF));
  const flush = () => {
    timer = 0;
    const d = draft();
    if (!d) return;
    const out = sendable(d);
    before = known;
    known = JSON.stringify(out);
    props.onSave(out);
  };
  onCleanup(() => {
    if (timer) {
      clearTimeout(timer);
      flush();
    }
  });

  // Follow the file when it changes underneath and nothing here is unsaved.
  // Tracks the pushed policy only: an edit must not re-run it.
  createEffect(() => {
    const k = key(props.policy);
    untrack(() => {
      if (!draft() || timer || k === known || k === before) return;
      setDraft(null);
    });
  });

  const edit = (next: agentproto.PolicyView) => {
    if (!untrack(draft)) {
      known = key(props.policy);
      before = known;
    }
    if (timer) clearTimeout(timer);
    timer = window.setTimeout(flush, POLICY_SAVE_DELAY_MS);
    setDraft(next);
  };
  const patch = (p: Partial<agentproto.PolicyView>) => edit({ ...view(), ...p });
  const setRules = (next: agentproto.PolicyRule[]) => patch({ rules: next });
  const editRule = (i: number, r: Partial<agentproto.PolicyRule>) =>
    setRules(rules().map((cur, idx) => (idx === i ? { ...cur, ...r } : cur)));
  const addRule = () => setRules([...rules(), { match: '', decision: 'ask' }]);
  const removeRule = (i: number) => setRules(rules().filter((_, idx) => idx !== i));
  // Order is meaning here (the first matching rule wins), so moving a row
  // is an edit, not a cosmetic sort.
  const moveRule = (i: number, delta: number) => {
    const next = [...rules()];
    const j = i + delta;
    if (j < 0 || j >= next.length) return;
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
  };

  return (
    <div data-testid="agents-permissions" style={{ display: 'flex', 'flex-direction': 'column', gap: '18px', padding: `${tokens.spaceMd}px` }}>
      <Section title="Approvals">
        <div style={proseStyle}>
          A session started here asks before its agent uses a tool. With the
          policy on, wash answers the requests the rules below cover, and
          brings the rest to you in the sidebar, where “always allow” adds the
          rule so you never answer it twice. It is off until you turn it on,
          and anything it can't answer still ends up in front of you.
        </div>
        <Row label="Answer for me">
          <Checkbox
            data-testid="agents-policy-enabled"
            checked={view().enabled}
            onChange={(v) => patch({ enabled: v })}
            label={view().enabled ? 'on: the rules below apply' : 'off: every request goes to you'}
          />
        </Row>
        <Row label="No rule matches">
          <Select
            data-testid="agents-policy-default"
            value={view().default === 'deny' ? 'deny' : 'ask'}
            options={DEFAULTS}
            onChange={(v) => patch({ default: v })}
          />
        </Row>
        <Row label="Ask me here">
          <Checkbox
            data-testid="agents-policy-ask-desktop"
            checked={view().ask_desktop}
            onChange={(v) => patch({ ask_desktop: v })}
            disabled={!view().enabled}
            label="show the question in the sidebar, with an “always allow” that adds the rule"
          />
        </Row>
      </Section>

      <Section title="Rules (first match wins)">
        <div style={proseStyle}>
          Match a tool by name, optionally with a pattern:{' '}
          <code style={codeStyle}>Read</code>, <code style={codeStyle}>Bash(git status:*)</code>,{' '}
          <code style={codeStyle}>Edit(/etc/*)</code>. <code style={codeStyle}>*</code> matches any
          text and <code style={codeStyle}>?</code> one character. A directory scopes the rule to
          work under that path.
        </div>
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
          <Show when={rules().length === 0}>
            <div data-testid="agents-policy-rules-empty" style={{ ...proseStyle, 'font-style': 'italic' }}>
              No rules: every request goes to you.
            </div>
          </Show>
          {/* Index, not For: rows keyed by position, so typing in a row
              (which replaces its rule object) doesn't recreate the input. */}
          <Index each={rules()}>
            {(r, i) => (
              <div data-testid="agents-policy-rule" style={ruleRowStyle}>
                <Input
                  data-testid="agents-policy-rule-match"
                  value={r().match}
                  placeholder="Bash(git status:*)"
                  onInput={(e) => editRule(i, { match: e.currentTarget.value })}
                  style={{ ...fieldStyle, 'font-family': 'monospace' }}
                />
                <Select
                  data-testid="agents-policy-rule-decision"
                  value={r().decision || 'ask'}
                  options={DECISIONS}
                  onChange={(v) => editRule(i, { decision: v })}
                />
                <Input
                  data-testid="agents-policy-rule-cwd"
                  value={r().cwd ?? ''}
                  placeholder="anywhere"
                  onInput={(e) => editRule(i, { cwd: e.currentTarget.value })}
                  style={fieldStyle}
                />
                <div style={{ display: 'flex', gap: '4px' }}>
                  <SmallBtn data-testid="agents-policy-rule-up" onClick={() => moveRule(i, -1)}>↑</SmallBtn>
                  <SmallBtn data-testid="agents-policy-rule-down" onClick={() => moveRule(i, 1)}>↓</SmallBtn>
                  <SmallBtn data-testid="agents-policy-rule-remove" onClick={() => removeRule(i)} title="Remove this rule">✕</SmallBtn>
                </div>
              </div>
            )}
          </Index>
          <div style={{ display: 'flex', gap: `${tokens.spaceSm}px`, 'align-items': 'center' }}>
            <SmallBtn data-testid="agents-policy-rule-add" onClick={addRule}>add rule</SmallBtn>
            <Show when={props.result?.ok === false}>
              <span data-testid="agents-policy-error" style={{ color: tokens.accentRed, font: tokens.type.textSm }}>
                Not saved: {props.result?.detail}
              </span>
            </Show>
          </div>
        </div>
      </Section>

      <div style={{ ...proseStyle, opacity: 0.6 }}>
        These rules cover sessions wash runs, the ones started from this
        window. An agent you start yourself in a terminal is not governed by
        them: wash is not in that conversation.
      </div>
    </div>
  );
};

const proseStyle: JSX.CSSProperties = {
  font: tokens.type.textMd,
  opacity: 0.75,
  'line-height': 1.5,
  'margin-bottom': '10px',
  'max-width': '58ch',
};

const codeStyle: JSX.CSSProperties = {
  font: tokens.type.monoMd,
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusSm,
  padding: '1px 5px',
};

const ruleRowStyle: JSX.CSSProperties = {
  display: 'grid',
  // minmax(0, …): the fields shrink to the Agents window's left column
  // rather than pushing the row's buttons out of it.
  'grid-template-columns': 'minmax(0, 2fr) 84px minmax(0, 1fr) auto',
  gap: '6px',
  'align-items': 'center',
};

const fieldStyle: JSX.CSSProperties = { width: '100%', 'min-width': 0, 'box-sizing': 'border-box' };

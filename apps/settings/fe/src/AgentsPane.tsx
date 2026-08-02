// Agents pane — the coding-agent approval policy (docs/AGENT_TERM.md §6).
//
// When a coding agent running in a wash terminal is about to use a tool,
// its PreToolUse hook asks the terminal what to do; the terminal answers
// from the ordered table edited here. The pane persists to the `agents`
// settings domain (~/.config/wash/agents.json) through the same host
// read/write path as every other wash config — no service, no live
// connection, and every wash-term picks the change up on its next
// question.
//
// The UI is deliberately blunt about what this is: a switch that lets a
// program act without asking you. It ships off, it explains what "on"
// means, and it never offers "allow everything" as a default — a
// hand-edited file can still say that, and the pane will show it, but the
// picker won't hand it to you.
//
// (This is a native pane rather than a define-settings-panel bundle
// because wash-term is InstancingMulti: the settings host addresses
// panels by app id, which the router only resolves for singletons. When
// M4's agentd singleton lands it can own a real panel; the domain file
// stays the same, so that's a UI move, not a data migration.)

import { For, Show, createSignal } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Checkbox, Input, Row, Section, Select, SmallBtn, tokens, washCopyText } from '@wash/ui';

/** One row of the policy table. */
export interface AgentRule {
  /** `Tool` or `Tool(glob)` — e.g. Read, Bash(git status:*), Edit(/etc/**) */
  match: string;
  /** allow | deny | ask */
  decision: string;
  /** optional: only applies at or under this directory */
  cwd?: string;
}

export interface AgentPolicy {
  enabled?: boolean;
  default?: string;
  rules?: AgentRule[];
  /** opt-in: watch hookless agents for (y/n) prompts and type y */
  legacy_autoapprove?: boolean;
  /** ask the desktop when no rule matches (default true while enabled) */
  ask_desktop?: boolean;
}

const DECISIONS: [string, string][] = [
  ['ask', 'ask me'],
  ['allow', 'allow'],
  ['deny', 'deny'],
];

// The unmatched-request decision. "allow" is deliberately not offered:
// a default-allow policy is indistinguishable from having no permission
// system at all, and nobody arrives at it by accident.
const DEFAULTS: [string, string][] = [
  ['ask', 'ask me (recommended)'],
  ['deny', 'deny'],
];

const INSTALL_CMD = 'wash agent-hooks install';

export const AgentsPane: Component<{
  policy: AgentPolicy;
  onChange: (p: AgentPolicy) => void;
}> = (props) => {
  const [copied, setCopied] = createSignal(false);
  const rules = (): AgentRule[] => props.policy.rules ?? [];

  const patch = (p: Partial<AgentPolicy>) => props.onChange({ ...props.policy, ...p });
  const setRules = (next: AgentRule[]) => patch({ rules: next });
  const editRule = (i: number, r: Partial<AgentRule>) =>
    setRules(rules().map((cur, idx) => (idx === i ? { ...cur, ...r } : cur)));
  const addRule = () => setRules([...rules(), { match: '', decision: 'ask' }]);
  const removeRule = (i: number) => setRules(rules().filter((_, idx) => idx !== i));
  // Order is meaning here — the first matching rule wins — so moving a
  // row up or down is an edit, not a cosmetic sort.
  const moveRule = (i: number, delta: number) => {
    const next = [...rules()];
    const j = i + delta;
    if (j < 0 || j >= next.length) return;
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
  };

  const copyInstall = () => {
    void washCopyText(INSTALL_CMD);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1_200);
  };

  return (
    <div data-testid="settings-agents" style={{ display: 'flex', 'flex-direction': 'column', gap: '18px' }}>
      <Section title="Coding agents">
        <div style={proseStyle}>
          A coding agent (Claude Code, Codex, …) running in a wash terminal
          asks before it uses a tool. With the policy on, wash answers the
          questions the table below covers, and brings the rest to you in
          the sidebar — where “always allow” writes the rule so you never
          answer it twice. It is off until you turn it on, and anything it
          can't answer still ends up in front of you.
        </div>
        <Row label="Answer for me">
          <Checkbox
            data-testid="agents-enabled"
            checked={props.policy.enabled === true}
            onChange={(v) => patch({ enabled: v })}
            label={props.policy.enabled ? 'on — rules below apply' : 'off — every request goes to you'}
          />
        </Row>
        <Row label="No rule matches">
          <Select
            data-testid="agents-default"
            value={props.policy.default === 'deny' ? 'deny' : 'ask'}
            options={DEFAULTS}
            onChange={(v) => patch({ default: v })}
          />
        </Row>
        <Row label="Ask me here">
          <Checkbox
            data-testid="agents-ask-desktop"
            checked={props.policy.ask_desktop !== false}
            onChange={(v) => patch({ ask_desktop: v })}
            disabled={props.policy.enabled !== true}
            label="show the question in the sidebar, with an “always allow” that writes the rule"
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
            <div data-testid="agents-rules-empty" style={{ ...proseStyle, 'font-style': 'italic' }}>
              No rules — every request goes to you.
            </div>
          </Show>
          <For each={rules()}>
            {(r, i) => (
              <div data-testid="agents-rule" style={ruleRowStyle}>
                <Input
                  data-testid="agents-rule-match"
                  value={r.match}
                  placeholder="Bash(git status:*)"
                  onInput={(e) => editRule(i(), { match: e.currentTarget.value })}
                  style={{ 'font-family': 'monospace' }}
                />
                <Select
                  data-testid="agents-rule-decision"
                  value={r.decision || 'ask'}
                  options={DECISIONS}
                  onChange={(v) => editRule(i(), { decision: v })}
                />
                <Input
                  data-testid="agents-rule-cwd"
                  value={r.cwd ?? ''}
                  placeholder="anywhere"
                  onInput={(e) => editRule(i(), { cwd: e.currentTarget.value })}
                />
                <div style={{ display: 'flex', gap: '4px' }}>
                  <SmallBtn data-testid="agents-rule-up" onClick={() => moveRule(i(), -1)}>↑</SmallBtn>
                  <SmallBtn data-testid="agents-rule-down" onClick={() => moveRule(i(), 1)}>↓</SmallBtn>
                  <SmallBtn data-testid="agents-rule-remove" onClick={() => removeRule(i())}>remove</SmallBtn>
                </div>
              </div>
            )}
          </For>
          <div>
            <SmallBtn data-testid="agents-rule-add" onClick={addRule}>add rule</SmallBtn>
          </div>
        </div>
      </Section>

      <Section title="Agents without hooks">
        <div style={proseStyle}>
          Some agents have no hooks to install. For those, wash can watch the
          terminal for a <code style={codeStyle}>(y/n)</code> question and type{' '}
          <code style={codeStyle}>y</code>. It only ever does this while an agent
          is the program running in that tab, but it is still pattern-matching
          text: anything printed in the terminal can imitate a prompt. Prefer the
          rules above wherever the agent supports hooks.
        </div>
        <Row label="Type y for me">
          <Checkbox
            data-testid="agents-legacy"
            checked={props.policy.legacy_autoapprove === true}
            onChange={(v) => patch({ legacy_autoapprove: v })}
            disabled={props.policy.enabled !== true}
            label={
              props.policy.enabled !== true
                ? 'needs "Answer for me" on'
                : props.policy.legacy_autoapprove
                  ? 'on — spoofable by design'
                  : 'off'
            }
          />
        </Row>
      </Section>

      <Section title="Hooks">
        <div style={proseStyle}>
          Agents report their state — and ask these questions — through hooks
          wash installs into the agent's own config. Run this once per box:
        </div>
        <div style={cmdRowStyle}>
          <code data-testid="agents-install-cmd" style={{ ...codeStyle, flex: 1 }}>{INSTALL_CMD}</code>
          <SmallBtn data-testid="agents-install-copy" onClick={copyInstall}>
            {copied() ? 'copied' : 'copy'}
          </SmallBtn>
        </div>
        <div style={{ ...proseStyle, opacity: 0.6 }}>
          <code style={codeStyle}>wash agent-hooks status</code> shows what is installed;{' '}
          <code style={codeStyle}>remove</code> takes it back out. The merge only ever touches
          entries wash added.
        </div>
      </Section>
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
  'grid-template-columns': 'minmax(160px, 2fr) 110px minmax(120px, 1fr) auto',
  gap: '8px',
  'align-items': 'center',
};

const cmdRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '8px',
  'margin-bottom': '10px',
  'max-width': '58ch',
};

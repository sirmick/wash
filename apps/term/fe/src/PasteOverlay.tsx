// Smart-paste preview (docs/AGENT_TERM.md §10).
//
// Shown only when analyzePaste returned `ask` — i.e. we would change the
// structure of what the user pasted, or it is multi-line and about to hit a
// shell. The overlay's whole job is informed consent: what we found, what
// we'd send instead, and three unambiguous ways out. "Paste as-is" is a
// first-class button, not a fallback, because the user is the one who knows
// what they copied.

import { For, Show } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { Button, Overlay, tokens } from '@wash/ui';
import type { PasteAnalysis } from '@wash/ui';

export interface PasteOverlayProps {
  analysis: PasteAnalysis;
  onCleaned: () => void;
  onAsIs: () => void;
  onCancel: () => void;
}

// PREVIEW_LINES caps the preview. A 400-line paste doesn't need to be read
// in full to be judged, and an unbounded block would push the buttons off
// the window.
const PREVIEW_LINES = 12;

function preview(text: string): { shown: string; hidden: number } {
  const lines = text.split('\n');
  if (lines.length <= PREVIEW_LINES) return { shown: text, hidden: 0 };
  return { shown: lines.slice(0, PREVIEW_LINES).join('\n'), hidden: lines.length - PREVIEW_LINES };
}

export const PasteOverlay: Component<PasteOverlayProps> = (props) => {
  const cleaned = () => preview(props.analysis.cleaned);
  const danger = () => props.analysis.issues.some((i) => i.kind === 'executes-immediately');
  return (
    <Overlay onDismiss={props.onCancel} data-testid="term-paste-overlay">
      <div style={cardStyle}>
        <div style={titleStyle}>
          {props.analysis.wrapped ? 'This looks like one wrapped command' : 'Check this paste'}
        </div>
        <ul data-testid="term-paste-issues" style={issuesStyle}>
          <For each={props.analysis.issues}>
            {(i) => (
              <li data-issue={i.kind} style={{ color: i.kind === 'executes-immediately' ? tokens.fgWarning : tokens.fg }}>
                {i.label}
              </li>
            )}
          </For>
        </ul>
        <Show when={danger()}>
          <div data-testid="term-paste-warning" style={warnStyle}>
            Bracketed paste is off in this program, so every line runs the moment
            it arrives — including the last one, with or without you pressing Enter.
          </div>
        </Show>
        <div style={labelStyle}>Will paste</div>
        <pre data-testid="term-paste-preview" style={preStyle}>{cleaned().shown}</pre>
        <Show when={cleaned().hidden > 0}>
          <div style={moreStyle}>…and {cleaned().hidden} more line(s)</div>
        </Show>
        <div style={rowStyle}>
          <Button data-testid="term-paste-cleaned" onClick={props.onCleaned}>
            Paste cleaned
          </Button>
          <Button data-testid="term-paste-asis" variant="ghost" onClick={props.onAsIs}>Paste as-is</Button>
          <Button data-testid="term-paste-cancel" variant="ghost" onClick={props.onCancel}>Cancel</Button>
        </div>
      </div>
    </Overlay>
  );
};

// The Overlay already supplies the card chrome (surface, border, padding);
// this is only the column layout inside it.
const cardStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '8px',
  'max-width': '560px',
  color: tokens.fg,
  font: tokens.type.textMd,
};

const titleStyle: JSX.CSSProperties = { font: tokens.type.titleSm };

const issuesStyle: JSX.CSSProperties = {
  margin: 0,
  padding: '0 0 0 18px',
  opacity: 0.85,
  'line-height': 1.5,
};

const warnStyle: JSX.CSSProperties = {
  background: tokens.bgWarning,
  color: tokens.fgWarning,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusSm,
  padding: '6px 8px',
  'line-height': 1.4,
};

const labelStyle: JSX.CSSProperties = {
  font: tokens.type.textSm,
  opacity: 0.6,
  'text-transform': 'uppercase',
  'letter-spacing': '0.04em',
};

const preStyle: JSX.CSSProperties = {
  margin: 0,
  padding: '8px',
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusSm,
  font: tokens.type.monoMd,
  'white-space': 'pre-wrap',
  'word-break': 'break-all',
  'max-height': '220px',
  overflow: 'auto',
};

const moreStyle: JSX.CSSProperties = { font: tokens.type.textSm, opacity: 0.6 };

const rowStyle: JSX.CSSProperties = {
  display: 'flex',
  gap: '8px',
  'justify-content': 'flex-end',
  'margin-top': '4px',
};

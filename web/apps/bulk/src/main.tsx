// wash-app-bulk: the singleton "Bulk Ops" UI — shows the queue
// of in-flight / completed jobs sent by other apps (fm, mostly).
// Each row has the op label, a progress bar, status, and a cancel
// button while running. Done/cancelled/failed rows stick around
// briefly then auto-clear so the queue settles when idle.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore } from 'solid-js/store';
import { Button, defineWashApp } from '@wash/ui';
import type { Component } from 'solid-js';

type Status = 'queued' | 'running' | 'done' | 'failed' | 'cancelled';
type Op = 'delete' | 'move' | 'copy';

interface JobView {
  job_id: string;
  op: Op;
  status: Status;
  paths: string[];
  dest: string;
  done: number;
  total: number;
  error: string;
}

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

// AUTO_CLEAR_MS is how long terminal-state rows stay before fading
// from the queue. Long enough to read, short enough that the
// window doesn't accumulate stale rows.
const AUTO_CLEAR_MS = 5_000;

interface Conflict {
  src: string;
  src_type: string; // "file" | "dir" | "symlink"
  dst: string;
  dst_type: string;
}

// Helpers for the conflict prompt — branch text + button set on
// the (src_type, dst_type) pair, matching Windows Explorer's
// language: "Merge?" for dir-vs-dir, "Replace?" for file-vs-file,
// destructive phrasing on type mismatch.
function isDirVsDir(c: Conflict): boolean {
  return c.src_type === 'dir' && c.dst_type === 'dir';
}

function conflictTitle(c: Conflict): string {
  if (isDirVsDir(c)) return 'Merge with existing folder?';
  if (c.src_type === c.dst_type) return 'Replace existing?';
  // Type mismatch — explicit, scary.
  const kind = (t: string) => (t === 'dir' ? 'folder' : t || 'item');
  return `Replace existing ${kind(c.dst_type)} with ${kind(c.src_type)}?`;
}

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [jobs, setJobs] = createStore<Record<string, JobView>>({});
  const [order, setOrder] = createSignal<string[]>([]);
  // Active conflict prompt per job_id. While set, the matching
  // JobRow renders the Replace / Skip / Cancel choices instead of
  // the normal cancel button.
  const [conflicts, setConflicts] = createStore<Record<string, Conflict>>({});
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const resolveConflict = (jobID: string, action: string) => {
    send({ kind: 'conflict_resolve', id: `cr-${jobID}-${Date.now()}`, job_id: jobID, action });
    setConflicts(jobID, undefined as unknown as Conflict);
  };

  const handleBE = (m: BEMessage) => {
    switch (m.kind) {
      case 'job.update': {
        const j = m as unknown as JobView;
        const known = !!jobs[j.job_id];
        setJobs(j.job_id, j);
        if (!known) setOrder([...order(), j.job_id]);
        if (j.status === 'done' || j.status === 'failed' || j.status === 'cancelled') {
          // Terminal job — drop any lingering conflict prompt.
          setConflicts(j.job_id, undefined as unknown as Conflict);
          setTimeout(() => {
            setOrder(order().filter((id) => id !== j.job_id));
            setJobs(j.job_id, undefined as unknown as JobView);
          }, AUTO_CLEAR_MS);
        }
        return;
      }
      case 'job.conflict': {
        const jobID = String(m.job_id);
        setConflicts(jobID, {
          src: String(m.src),
          src_type: String(m.src_type ?? ''),
          dst: String(m.dst),
          dst_type: String(m.dst_type ?? ''),
        });
        return;
      }
      case 'list_ok': {
        const list = (m.jobs as JobView[]) ?? [];
        for (const j of list) setJobs(j.job_id, j);
        setOrder(list.map((j) => j.job_id));
        return;
      }
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
    // Seed the queue from the BE on mount (covers the case where
    // bulk-ops was already running when this window opened).
    send({ kind: 'list', id: 'init' });
  });

  const cancel = (jobID: string) => send({ kind: 'cancel', id: `c-${jobID}`, job_id: jobID });

  return (
    <div
      data-testid="bulk-root"
      style={{
        height: '100%',
        'box-sizing': 'border-box',
        padding: '8px 12px',
        color: '#eee',
        font: '13px ui-sans-serif,system-ui,sans-serif',
        background: '#10101a',
        overflow: 'auto',
      }}
    >
      <div style={{ opacity: 0.6, 'margin-bottom': '6px', font: '11px ui-monospace,Menlo,Consolas,monospace' }}>
        <span data-testid="bulk-count">{order().length}</span> job(s)
      </div>
      <Show when={order().length === 0}>
        <div style={{ opacity: 0.4, padding: '12px 0', 'font-style': 'italic' }}>queue is empty</div>
      </Show>
      <For each={order()}>
        {(id) => (
          <JobRow
            job={jobs[id]}
            conflict={conflicts[id]}
            onCancel={() => cancel(id)}
            onResolveConflict={(action) => resolveConflict(id, action)}
          />
        )}
      </For>
    </div>
  );
};

const JobRow: Component<{
  job: JobView;
  conflict?: Conflict;
  onCancel: () => void;
  onResolveConflict: (action: string) => void;
}> = (props) => {
  const fraction = () => {
    if (!props.job.total) return props.job.status === 'done' ? 1 : 0;
    return Math.min(1, props.job.done / props.job.total);
  };
  const label = () => `${props.job.op} ${props.job.paths.length} item${props.job.paths.length === 1 ? '' : 's'}${
    props.job.dest ? ` → ${props.job.dest}` : ''
  }`;
  const isTerminal = () => props.job.status === 'done' || props.job.status === 'failed' || props.job.status === 'cancelled';
  return (
    <div
      data-testid={`bulk-job-${props.job.job_id}`}
      data-status={props.job.status}
      style={{
        'border-top': '1px solid #2a2a3a',
        padding: '6px 0',
        display: 'grid',
        'grid-template-columns': '1fr auto',
        'column-gap': '8px',
        'row-gap': '3px',
        'align-items': 'center',
      }}
    >
      <div style={{ overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap' }}>
        {label()}
      </div>
      <Show
        when={!isTerminal()}
        fallback={
          <span data-testid={`bulk-status-${props.job.job_id}`} style={{ font: '11px ui-monospace,Menlo,Consolas,monospace', opacity: 0.6 }}>
            {props.job.status}
          </span>
        }
      >
        <button
          type="button"
          data-testid={`bulk-cancel-${props.job.job_id}`}
          onClick={props.onCancel}
          style={{
            background: 'transparent',
            color: '#eee',
            border: '1px solid #2a2a3a',
            'border-radius': '3px',
            padding: '2px 8px',
            cursor: 'pointer',
            font: '12px ui-sans-serif,system-ui,sans-serif',
          }}
        >
          cancel
        </button>
      </Show>
      <div
        data-testid={`bulk-progress-${props.job.job_id}`}
        data-fraction={fraction().toFixed(3)}
        style={{
          'grid-column': '1 / span 2',
          height: '6px',
          background: '#1d1d30',
          'border-radius': '3px',
          overflow: 'hidden',
          position: 'relative',
        }}
      >
        <div
          style={{
            width: `${Math.round(fraction() * 100)}%`,
            height: '100%',
            background: props.job.status === 'failed' ? '#a02d2d' : '#4a6ab0',
            transition: 'width 0.15s linear',
          }}
        />
      </div>
      <Show when={props.job.error}>
        <div
          style={{
            'grid-column': '1 / span 2',
            opacity: 0.7,
            font: '11px ui-monospace,Menlo,Consolas,monospace',
            color: '#e0a0a0',
            'word-break': 'break-all',
          }}
        >
          {props.job.error}
        </div>
      </Show>
      <Show when={props.conflict}>
        <div
          data-testid={`bulk-conflict-${props.job.job_id}`}
          style={{
            'grid-column': '1 / span 2',
            background: '#1d1d40',
            border: '1px solid #3a3a6a',
            'border-radius': '4px',
            padding: '8px 10px',
            'margin-top': '4px',
            animation: 'wash-pop-in 140ms ease-out',
          }}
        >
          <div style={{ 'margin-bottom': '6px', font: '12px ui-sans-serif,system-ui,sans-serif' }}>
            {conflictTitle(props.conflict!)}
          </div>
          <div
            style={{
              font: '11px ui-monospace,Menlo,Consolas,monospace',
              opacity: 0.8,
              'word-break': 'break-all',
              'margin-bottom': '8px',
            }}
          >
            {props.conflict!.dst}
          </div>
          <div style={{ display: 'flex', gap: '6px', 'flex-wrap': 'wrap' }}>
            <Show
              when={isDirVsDir(props.conflict!)}
              fallback={
                <>
                  <Button data-testid={`bulk-conflict-replace-${props.job.job_id}`} variant="danger" size="sm" onClick={() => props.onResolveConflict('replace')}>Replace</Button>
                  <Button data-testid={`bulk-conflict-replace-all-${props.job.job_id}`} variant="danger" size="sm" onClick={() => props.onResolveConflict('replace_all')}>Replace All</Button>
                </>
              }
            >
              <Button data-testid={`bulk-conflict-merge-${props.job.job_id}`} variant="ghost" size="sm" onClick={() => props.onResolveConflict('merge')}>Merge</Button>
              <Button data-testid={`bulk-conflict-merge-all-${props.job.job_id}`} variant="ghost" size="sm" onClick={() => props.onResolveConflict('merge_all')}>Merge All</Button>
              <Button data-testid={`bulk-conflict-replace-${props.job.job_id}`} variant="danger" size="sm" onClick={() => props.onResolveConflict('replace')}>Replace</Button>
            </Show>
            <Button data-testid={`bulk-conflict-skip-${props.job.job_id}`} variant="ghost" size="sm" onClick={() => props.onResolveConflict('skip')}>Skip</Button>
            <Button data-testid={`bulk-conflict-skip-all-${props.job.job_id}`} variant="ghost" size="sm" onClick={() => props.onResolveConflict('skip_all')}>Skip All</Button>
            <Button data-testid={`bulk-conflict-cancel-${props.job.job_id}`} variant="ghost" size="sm" onClick={() => props.onResolveConflict('cancel')}>Cancel</Button>
          </div>
        </div>
      </Show>
    </div>
  );
};

defineWashApp('wash-app-bulk', (props) => <App {...props} />);

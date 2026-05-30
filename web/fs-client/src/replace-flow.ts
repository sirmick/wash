// Replace-on-conflict retry flow, lifted from fm's App() closure.
// Framework-free (no DOM, no Solid): the BE round-trip and the user
// confirm are both injected, so the tricky async retry logic —
// previously reachable only through fm's fm-replace browser e2e — gets
// millisecond node:test coverage with a fake request + fake confirm.
// Shared: edit reimplements the same flow (see docs/FE_REFACTOR_PLAN.md).

import type { BEMessage } from './bus.ts';

export interface ReplaceFlowDeps {
  // request sends a BE op and resolves with its reply — in fm this is
  // the bus's request (request/reply correlation lives in bus.ts).
  request: (req: Record<string, unknown>) => Promise<BEMessage>;
  // confirm opens the Replace prompt for destPath and resolves with the
  // user's choice (true = overwrite, false = cancel).
  confirm: (destPath: string) => Promise<boolean>;
}

// withReplacePrompt runs req; if it comes back as `<errKind>` with
// code 'exists', it asks the user. On confirm it retries with
// `replace: true`; on cancel it returns a synthetic `{ kind: 'cancelled' }`
// so the caller can stay silent instead of surfacing an error. Any other
// reply (success, or a different error) is returned untouched without
// prompting.
export async function withReplacePrompt(
  deps: ReplaceFlowDeps,
  req: Record<string, unknown>,
  destPath: string,
  errKind: string,
): Promise<BEMessage> {
  let reply = await deps.request(req);
  if (reply.kind === errKind && reply.code === 'exists') {
    const confirmed = await deps.confirm(destPath);
    if (!confirmed) {
      return { kind: 'cancelled' };
    }
    reply = await deps.request({ ...req, replace: true });
  }
  return reply;
}

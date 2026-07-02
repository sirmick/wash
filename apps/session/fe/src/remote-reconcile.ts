// reconcileRemoteAttachments keeps the shell's remote-host attachments in
// sync with com.wash.remote's published host list. It runs in the always-alive
// session FE so remote windows re-attach after an SSH blip even with the
// wash-connect window closed (REVIEW-RECONNECT M4): a host cycling
// up→reconnecting→up in remote.state drives a fresh attach.
//
// It's a pure diff over the desired set (hosts whose status is "up") vs the
// `attached` set it mutates: attach() for each newly-up origin, detach() for
// each previously-attached origin no longer up. attach/detach are idempotent
// router-side, so running this alongside wash-connect's own reconcile
// converges without churn.

export interface ReconcileHost {
  origin: string;
  status: string; // starting | up | reconnecting | down
}

export interface ReconcileActions {
  attach: (origin: string) => void;
  detach: (origin: string) => void;
}

export function reconcileRemoteAttachments(
  list: ReconcileHost[],
  attached: Set<string>,
  actions: ReconcileActions,
): void {
  const live = new Set<string>();
  for (const h of list) {
    if (h.status === 'up') {
      live.add(h.origin);
      if (!attached.has(h.origin)) {
        actions.attach(h.origin);
        attached.add(h.origin);
      }
    }
  }
  for (const origin of [...attached]) {
    if (!live.has(origin)) {
      actions.detach(origin);
      attached.delete(origin);
    }
  }
}

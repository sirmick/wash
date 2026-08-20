// wash-app-priv — the approval queue, drawn where the escalation is
// (docs/SIDEBAR.md M4).
//
// This is a MODAL-surface app: it autoboots with the session, has no
// launcher entry, and paints only when the user summons it. The shell
// draws it above every window with the desktop blurred, and stamps the
// host label itself from app.declared — the router's word, which no app
// can forge. So a prompt that appears unbidden, or one with no host
// label, is by construction not this.
//
// Why it exists at all: the rail's copy of this queue reaches wash-priv
// through the session BE gateway, which resolves inside its OWN router.
// An escalation raised on a remote host could therefore never be
// answered from the desktop — and before M4a it wasn't even visible.
// This app is a normal app talking to its own host's service, so
// answering is correct for whichever host summoned it.
//
// The password is a bonus: it now transits priv's own FE rather than the
// session app's, which the old sidebar overlay's header comment flagged
// as a trust hop worth knowing about. It never crosses an app boundary.

import { createSignal } from 'solid-js';
import {
  PrivWidget, PrivUnlockOverlay, createAppBus, defineWashApp, tokens,
  type PrivReq, type PrivUnlockState, type WashAppProps,
} from '@wash/ui';

interface PrivState {
  locked?: boolean;
  queue?: PrivReq[];
  app_grants?: string[];
}

function PrivApp(props: WashAppProps) {
  const [reqs, setReqs] = createSignal<PrivReq[]>([]);
  const [grants, setGrants] = createSignal<string[]>([]);
  const [locked, setLocked] = createSignal(true);
  const [unlock, setUnlock] = createSignal<PrivUnlockState | null>(null);
  const [unlockErr, setUnlockErr] = createSignal('');

  // wash-priv broadcasts every queue change to its own FE as well as to
  // its cross-app subscribers, so there is nothing to subscribe to here —
  // being the service's FE is the subscription.
  const onMsg = (data: Record<string, unknown>) => {
    switch (data.kind) {
      case 'state': {
        const st = (data.state ?? data) as PrivState;
        setReqs(st.queue ?? []);
        setGrants(st.app_grants ?? []);
        setLocked(st.locked ?? true);
        return;
      }
      case 'req.new':
      case 'req.update': {
        // Incremental: fold the one row rather than waiting for a
        // snapshot, so the list moves while you are looking at it.
        const req = data.req as PrivReq | undefined;
        if (!req) return;
        setReqs((prev) => {
          const i = prev.findIndex((r) => r.req_id === req.req_id);
          if (i < 0) return [...prev, req];
          const next = prev.slice();
          next[i] = req;
          return next;
        });
        return;
      }
      case 'need_password': {
        setUnlockErr('');
        setUnlock({
          req_id: String(data.req_id ?? ''),
          be_pubkey: String(data.be_pubkey ?? ''),
        });
        return;
      }
      case 'priv.unlocked':
      case 'unlocked': {
        setLocked(false);
        setUnlock(null);
        setUnlockErr('');
        return;
      }
      case 'priv.locked':
      case 'locked': {
        setLocked(true);
        setUnlock(null);
        return;
      }
      case 'error': {
        if (unlock()) setUnlockErr(String(data.msg ?? 'unlock failed'));
        return;
      }
    }
  };

  // The bus wires wash:msg during setup; createAppBus owns the cleanup.
  const bus = createAppBus(props, { onMsg: (m) => onMsg(m as Record<string, unknown>) });
  // Ask for a snapshot immediately: the modal is summoned long after the
  // service booted, so all the interesting state already happened.
  bus.send({ kind: 'get_state' });

  return (
    <div style={{ padding: `${tokens.spaceMd}px`, 'min-width': '360px' }} data-testid="priv-app">
      <PrivWidget
        reqs={reqs}
        grants={grants}
        locked={locked}
        onApprove={(id) => bus.send({ kind: 'approve', req_id: id })}
        onApproveApp={(id) => bus.send({ kind: 'approve_app', req_id: id })}
        onRevokeApp={(appID) => bus.send({ kind: 'revoke_app', app_id: appID })}
        onReject={(id, reason) => bus.send({ kind: 'reject', req_id: id, reason })}
        onLock={() => bus.send({ kind: 'lock' })}
      />
      {/* The password never leaves this component, and this component is
          wash-priv's own bundle — so the plaintext never crosses an app
          boundary at all. The overlay renders itself only when state is
          non-null, so it needs no Show around it. */}
      <PrivUnlockOverlay
        state={unlock}
        error={unlockErr}
        onUnlock={(req) =>
          bus.send({
            kind: 'unlock',
            ciphertext: req.ciphertext,
            fe_pubkey: req.fe_pubkey,
            nonce: req.nonce,
          })
        }
        onCancel={(reqID) => {
          setUnlock(null);
          setUnlockErr('');
          bus.send({ kind: 'reject', req_id: reqID, reason: 'cancelled' });
        }}
      />
    </div>
  );
}

defineWashApp('wash-app-priv', PrivApp);

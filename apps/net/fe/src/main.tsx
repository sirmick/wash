// wash-app-net — the windowed network UI (docs/NET.md §2.11, §6, B1c).
//
// B1 ships the generated Advanced editor wired end-to-end: an Interface object
// edited through the descriptor-driven <ObjectForm>, validated and applied
// server-side by com.wash.netd (relayed through this app's unprivileged BE,
// apps/net/be). Edits debounce-validate; Apply runs the commit-confirm
// transaction and the result drives a Confirm/Discard prompt. The bespoke
// Tier-C screens (firewall matrix, devices, recipe wizards) and the apply
// terminal land in later rungs; this proves the round-trip the whole product
// stands on: FE edit → netd validate/apply → diagnostics back onto the widget.

import { Show, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { Button, defineWashApp, type WashAppProps } from "@wash/ui";

import { ObjectForm } from "./ObjectForm.tsx";
import type { Descriptor, Diagnostic } from "./objectform-model.ts";
import descriptorJson from "./generated/descriptor.json";
import i18nJson from "./generated/i18n.json";

const desc = descriptorJson as unknown as Descriptor;
const i18n = i18nJson as Record<string, string>;
const label = (key: string) => i18n[key] ?? key.split(".").pop() ?? key;
const ifaceDesc = desc.objects.find((o) => o.kind === "network/interface")!;

// The editor edits a single Interface; netd validates the whole Config, so we
// wrap it as Interfaces[0] — which also fixes the diagnostic path prefix.
const PREFIX = "Interfaces[0]";

// setAtPath immutably sets a dotted path (relative to the object) on a clone.
// Union sub-fields arrive as "Proto.IPAddr"; a union switch as "Proto" with a
// {_tag} value (replacing the variant, dropping the old variant's fields).
function setAtPath(obj: Record<string, any>, keys: string[], v: unknown): Record<string, any> {
  const [head, ...rest] = keys;
  const next = { ...obj };
  next[head] = rest.length === 0 ? v : setAtPath((obj[head] as Record<string, any>) ?? {}, rest, v);
  return next;
}

function isError(d: Diagnostic): boolean {
  return d.severity === "error" || d.severity === 0;
}

function NetApp(props: WashAppProps) {
  const [value, setValue] = createSignal<Record<string, any>>({
    Name: "lan",
    Device: "br-lan",
    Proto: { _tag: "static", IPAddr: "192.168.1.1/24" },
  });
  const [diags, setDiags] = createSignal<Diagnostic[]>([]);
  const [status, setStatus] = createSignal("idle");
  const [busy, setBusy] = createSignal(false);
  const [showAdvanced, setShowAdvanced] = createSignal(false);

  const hasErrors = createMemo(() => diags().some(isError));
  const awaitingConfirm = () => status() === "await-confirm";
  const wrap = () => ({ Interfaces: [value()] });

  // --- request/reply over app_msg (correlated by id) ----------------------
  let reqSeq = 0;
  const pending = new Map<string, (m: any) => void>();
  const sendWithReply = (kind: string, fields: Record<string, unknown> = {}, timeoutMs = 5000): Promise<any> => {
    reqSeq += 1;
    const id = `f-${reqSeq}`;
    return new Promise((resolve) => {
      const timer = window.setTimeout(() => {
        if (pending.delete(id)) resolve({ kind: `${kind}_err`, id, code: "timeout", msg: `no reply within ${timeoutMs}ms` });
      }, timeoutMs);
      pending.set(id, (m) => {
        window.clearTimeout(timer);
        resolve(m);
      });
      window.wash.sendAppMsg(props.instance, { kind, id, ...fields });
    });
  };

  const validate = async () => {
    const r = await sendWithReply("validate", { config: wrap() });
    if (r.kind === "validate_ok") setDiags(r.diagnostics ?? []);
  };
  const apply = async () => {
    setBusy(true);
    const r = await sendWithReply("apply", { config: wrap() });
    setBusy(false);
    if (r.kind === "apply_ok") {
      setStatus(r.state ?? "");
      if (Array.isArray(r.diagnostics)) setDiags(r.diagnostics);
    } else {
      setStatus(`error: ${r.msg ?? r.code}`);
    }
  };
  const finish = (kind: "confirm" | "revert") => async () => {
    const r = await sendWithReply(kind);
    setStatus(r.kind === `${kind}_ok` ? (r.state ?? "") : `error: ${r.msg ?? r.code}`);
  };

  // Debounced validate-on-edit.
  let vt: number | undefined;
  const onChange = (path: string, v: unknown) => {
    const rel = path.startsWith(`${PREFIX}.`) ? path.slice(PREFIX.length + 1) : path;
    setValue((prev) => setAtPath(prev, rel.split("."), v));
    if (vt) window.clearTimeout(vt);
    vt = window.setTimeout(validate, 250);
  };

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail;
      const id = m?.id;
      if (typeof id === "string" && pending.has(id)) {
        const cb = pending.get(id)!;
        pending.delete(id);
        cb(m);
      }
    };
    props.host.addEventListener("wash:msg", onMsg);
    void validate();
    onCleanup(() => props.host.removeEventListener("wash:msg", onMsg));
  });

  return (
    <div class="wash-net-app">
      <style>{STYLE}</style>
      <header class="wash-net-head">
        <h1>Network</h1>
        <label class="wash-net-adv">
          <input type="checkbox" checked={showAdvanced()} onChange={(e) => setShowAdvanced(e.currentTarget.checked)} />
          Advanced
        </label>
      </header>

      <ObjectForm
        object={ifaceDesc}
        value={value()}
        diagnostics={diags()}
        showAdvanced={showAdvanced()}
        pathPrefix={PREFIX}
        label={label}
        onChange={onChange}
      />

      <footer class="wash-net-foot">
        <span class="wash-net-status" data-status={status()}>{status()}</span>
        <Show
          when={awaitingConfirm()}
          fallback={
            <Button onClick={apply} disabled={busy() || hasErrors()}>
              {busy() ? "Applying…" : "Apply"}
            </Button>
          }
        >
          <span class="wash-net-confirm-note">Keep these changes?</span>
          <Button variant="ghost" onClick={finish("revert")}>Discard</Button>
          <Button onClick={finish("confirm")}>Keep</Button>
        </Show>
      </footer>
    </div>
  );
}

const STYLE = `
.wash-net-app { display:flex; flex-direction:column; height:100%; }
.wash-net-head { display:flex; align-items:center; justify-content:space-between; padding:8px 12px; border-bottom:1px solid #2a2d33; }
.wash-net-head h1 { font-size:14px; font-weight:600; margin:0; }
.wash-net-adv { font-size:12px; opacity:.8; display:flex; gap:4px; align-items:center; }
.wash-net-form { flex:1; overflow:auto; padding:8px 12px; }
.wash-net-group { border:1px solid #2a2d33; border-radius:6px; margin:0 0 10px; padding:6px 10px 10px; }
.wash-net-group legend { font-size:11px; text-transform:uppercase; letter-spacing:.05em; opacity:.7; padding:0 4px; }
.wash-net-field { display:grid; grid-template-columns:140px 1fr; align-items:center; gap:8px; margin:5px 0; }
.wash-net-field.error input, .wash-net-field.error select { outline:1px solid #e06060; }
.wash-net-label { font-size:12px; opacity:.85; }
.wash-net-field input, .wash-net-field select, .wash-net-field textarea { background:#1b1d22; color:#cfd0d4; border:1px solid #33363d; border-radius:4px; padding:3px 6px; font:inherit; }
.wash-net-diag { grid-column:2; font-size:11px; color:#e06060; }
.wash-net-diag[data-severity="warning"] { color:#d0a040; }
.wash-net-union { display:flex; flex-direction:column; gap:6px; }
.wash-net-foot { display:flex; align-items:center; gap:8px; padding:8px 12px; border-top:1px solid #2a2d33; }
.wash-net-status { flex:1; font-size:12px; opacity:.8; font-variant:tabular-nums; }
.wash-net-confirm-note { font-size:12px; color:#d0a040; }
`;

defineWashApp("wash-app-net", NetApp, { style: "display:block;height:100%;overflow:hidden;font:13px/1.5 ui-sans-serif,system-ui,sans-serif;color:#cfd0d4;" });

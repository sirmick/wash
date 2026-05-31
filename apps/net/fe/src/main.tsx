// wash-app-net — the windowed network UI (docs/NET.md §2.11, §6, B5).
//
// The type-2 (workstation) settings app: a Connections list loaded from the
// box's live state (netd `current`, which reads real NetworkManager), an
// `+ Add` menu of recipe-style wizards (VLAN, Bridge, …) gated by the backend's
// capabilities, and the commit-confirm apply terminal. Adding a VLAN or bridging
// NICs is a multi-object change (a Device + an Interface, members become ports),
// so those are wizards, not single-object edits; the generic <ObjectForm> stays
// the Advanced editor for one interface's addressing. Every change runs through
// netd validate → apply (commit-confirm) → the box.

import { createMemo, createSignal, onCleanup, onMount, For, Show } from "solid-js";
import { defineWashApp, type WashAppProps } from "@wash/ui";

import { ApplyTerminal, type ApplyEvent } from "./ApplyTerminal.tsx";


type Proto = { _tag: string; IPAddr?: string; Gateway?: string; DNS?: string[]; Hostname?: string };
type Interface = { Name: string; Device?: string; Proto?: Proto };
type Device = { Name: string; Type?: string; Ports?: string[]; Ifname?: string; VID?: number };
type Config = { Interfaces?: Interface[]; Devices?: Device[]; [k: string]: any };
type Caps = {
  can_bridge?: boolean; can_vlan?: boolean; can_wireguard?: boolean;
  can_wifi_client?: boolean; can_zones?: boolean; can_dhcp_server?: boolean; can_ap?: boolean;
};

const protoLabel = (p?: Proto): string => {
  if (!p) return "—";
  switch (p._tag) {
    case "static": return `static ${p.IPAddr ?? ""}`;
    case "dhcp": return "DHCP";
    case "none": return "no IP";
    case "wireguard": return "WireGuard";
    default: return p._tag;
  }
};

function NetApp(props: WashAppProps) {
  const [config, setConfig] = createSignal<Config>({ Interfaces: [], Devices: [] });
  const [caps, setCaps] = createSignal<Caps>({});
  const [links, setLinks] = createSignal<string[]>([]); // physical NICs from the backend
  const [adding, setAdding] = createSignal<null | "vlan" | "bridge">(null);

  const [status, setStatus] = createSignal("idle");
  const [events, setEvents] = createSignal<ApplyEvent[]>([]);
  const [confirmWindowMs, setConfirmWindowMs] = createSignal(0);
  const [deadline, setDeadline] = createSignal(0);
  const [remaining, setRemaining] = createSignal(0);
  const [busy, setBusy] = createSignal(false);

  const devByName = createMemo(() => {
    const m = new Map<string, Device>();
    for (const d of config().Devices ?? []) m.set(d.Name, d);
    return m;
  });
  // Physical NICs available as VLAN parents / bridge members: the backend's
  // managed links, minus any already consumed by a bridge/vlan or carrying a
  // standalone interface.
  const freeDevices = createMemo(() => {
    const taken = new Set<string>();
    for (const d of config().Devices ?? []) {
      (d.Ports ?? []).forEach((p) => taken.add(p));
      if (d.Ifname) taken.add(d.Ifname);
    }
    for (const i of config().Interfaces ?? []) {
      if (i.Device) taken.add(i.Device);
    }
    return links().filter((d) => !taken.has(d));
  });

  // --- request/reply over app_msg (correlated by id) ----------------------
  let reqSeq = 0;
  const pending = new Map<string, (m: any) => void>();
  const sendWithReply = (kind: string, fields: Record<string, unknown> = {}, timeoutMs = 8000): Promise<any> => {
    reqSeq += 1;
    const id = `f-${reqSeq}`;
    return new Promise((resolve) => {
      const timer = window.setTimeout(() => {
        if (pending.delete(id)) resolve({ kind: `${kind}_err`, id, code: "timeout", msg: `no reply within ${timeoutMs}ms` });
      }, timeoutMs);
      pending.set(id, (m) => { window.clearTimeout(timer); resolve(m); });
      window.wash.sendAppMsg(props.instance, { kind, id, ...fields });
    });
  };

  const applyState = (s: any) => {
    if (!s) return;
    if (typeof s.status === "string") setStatus(s.status);
    if (Array.isArray(s.events)) setEvents(s.events as ApplyEvent[]);

    if (typeof s.confirm_window_ms === "number") {
      setConfirmWindowMs(s.confirm_window_ms);
      setDeadline(s.status === "await-confirm" && s.confirm_window_ms > 0 ? Date.now() + s.confirm_window_ms : 0);
    }
  };

  const loadCurrent = async () => {
    const r = await sendWithReply("current");
    if (r.kind === "current_ok") {
      setConfig((r.config ?? { Interfaces: [], Devices: [] }) as Config);
      setCaps((r.caps ?? {}) as Caps);
      setLinks((r.devices ?? []) as string[]);
    }
  };

  // applyConfig stages a whole new Config (current + the wizard's objects) and
  // runs the commit-confirm transaction.
  const applyConfig = async (next: Config) => {
    setBusy(true);
    setStatus("applying"); setEvents([]);
    const r = await sendWithReply("apply", { config: next }, 30000);
    setBusy(false);
    if (r.kind === "apply_ok") {
      applyState({ status: r.state, events: r.events, diagnostics: r.diagnostics, confirm_window_ms: r.confirm_window_ms });
    } else {
      setStatus(`error: ${r.msg ?? r.code}`);
    }
  };

  const finish = (kind: "confirm" | "revert") => async () => {
    const r = await sendWithReply(kind, {}, 30000);
    if (r.kind === `${kind}_ok`) setStatus(r.state ?? "");
    else setStatus(`error: ${r.msg ?? r.code}`);
    await loadCurrent(); // refresh the list from the (now committed/reverted) box
  };

  const removeConnection = (iface: Interface) => {
    const next = structuredClone(config());
    next.Interfaces = (next.Interfaces ?? []).filter((i) => i.Name !== iface.Name);
    const dev = devByName().get(iface.Device ?? "");
    if (dev) next.Devices = (next.Devices ?? []).filter((d) => d.Name !== dev.Name);
    void applyConfig(next);
  };

  // --- wizard submit: build the multi-object change, then apply -------------
  const addVLAN = (parent: string, vid: number, proto: Proto) => {
    const dev = `${parent}.${vid}`;
    const next = structuredClone(config());
    next.Devices = [...(next.Devices ?? []), { Name: dev, Type: "8021q", Ifname: parent, VID: vid }];
    next.Interfaces = [...(next.Interfaces ?? []), { Name: `${parent}_${vid}`, Device: dev, Proto: proto }];
    setAdding(null);
    void applyConfig(next);
  };
  const addBridge = (name: string, members: string[], proto: Proto) => {
    const next = structuredClone(config());
    const isMember = new Set(members);
    // members become ports — drop their standalone interfaces
    next.Interfaces = (next.Interfaces ?? []).filter((i) => !isMember.has(i.Device ?? ""));
    next.Devices = [...(next.Devices ?? []), { Name: name, Type: "bridge", Ports: members }];
    next.Interfaces = [...next.Interfaces, { Name: name, Device: name, Proto: proto }];
    setAdding(null);
    void applyConfig(next);
  };

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail;
      if (!m) return;
      if (m.kind === "net.state") { applyState(m.state); return; }
      const id = m?.id;
      if (typeof id === "string" && pending.has(id)) {
        const cb = pending.get(id)!; pending.delete(id); cb(m);
      }
    };
    props.host.addEventListener("wash:msg", onMsg);
    const tick = window.setInterval(() => {
      const d = deadline();
      setRemaining(d ? Math.max(0, d - Date.now()) : 0);
    }, 250);
    void loadCurrent();
    onCleanup(() => {
      props.host.removeEventListener("wash:msg", onMsg);
      window.clearInterval(tick);
    });
  });

  const kindOf = (iface: Interface): string => {
    const d = devByName().get(iface.Device ?? "");
    if (d?.Type === "bridge") return "Bridge";
    if (d?.Type === "8021q") return `VLAN ${d.VID}`;
    if (iface.Proto?._tag === "wireguard") return "WireGuard";
    return "Ethernet";
  };

  return (
    <div class="wash-net-app">
      <style>{STYLE}</style>
      <header class="wash-net-head">
        <h1>Network</h1>
        <div class="wash-net-add">
          <button class="wash-net-btn" disabled={adding() !== null || busy() || !caps().can_vlan} onClick={() => setAdding("vlan")}>+ VLAN</button>
          <button class="wash-net-btn" disabled={adding() !== null || busy() || !caps().can_bridge} onClick={() => setAdding("bridge")}>+ Bridge</button>
        </div>
      </header>

      <div class="wash-net-body">
        <Show when={adding() === "vlan"}>
          <VLANWizard parents={freeDevices()} onCancel={() => setAdding(null)} onCreate={addVLAN} />
        </Show>
        <Show when={adding() === "bridge"}>
          <BridgeWizard members={freeDevices()} onCancel={() => setAdding(null)} onCreate={addBridge} />
        </Show>

        <div class="wash-net-list">
          <For each={config().Interfaces ?? []} fallback={<div class="wash-net-empty">No connections yet — use + VLAN / + Bridge.</div>}>
            {(iface) => {
              const d = devByName().get(iface.Device ?? "");
              return (
                <div class="wash-net-conn">
                  <div class="wash-net-conn-main">
                    <span class="wash-net-conn-name">{iface.Name}</span>
                    <span class="wash-net-conn-kind">{kindOf(iface)}</span>
                    <span class="wash-net-conn-dev">{iface.Device ?? "—"}</span>
                  </div>
                  <div class="wash-net-conn-sub">
                    {protoLabel(iface.Proto)}
                    <Show when={d?.Type === "bridge"}> · ports: {(d?.Ports ?? []).join(", ")}</Show>
                  </div>
                  <button class="wash-net-btn ghost" disabled={busy()} onClick={() => removeConnection(iface)}>Remove</button>
                </div>
              );
            }}
          </For>
        </div>

        <div class="wash-net-greyed">
          <Show when={!caps().can_zones}><span class="wash-net-lock">Firewall 🔒</span></Show>
          <Show when={!caps().can_dhcp_server}><span class="wash-net-lock">DHCP server 🔒</span></Show>
          <Show when={!caps().can_ap}><span class="wash-net-lock">Access point 🔒</span></Show>
          <span class="wash-net-hint">available in router mode</span>
        </div>
      </div>

      <ApplyTerminal
        status={status}
        events={events}
        remainingMs={remaining}
        windowMs={confirmWindowMs}
        busy={busy}
        canApply={() => false}
        onApply={() => {}}
        onKeep={finish("confirm")}
        onDiscard={finish("revert")}
      />
    </div>
  );
}

// --- wizards ---------------------------------------------------------------

function AddressingFields(p: { proto: () => Proto; setProto: (x: Proto) => void }) {
  return (
    <>
      <label class="wash-net-field">
        <span class="wash-net-label">IPv4</span>
        <select value={p.proto()._tag} onChange={(e) => p.setProto(e.currentTarget.value === "static" ? { _tag: "static", IPAddr: "" } : { _tag: "dhcp" })}>
          <option value="dhcp">Automatic (DHCP)</option>
          <option value="static">Manual</option>
        </select>
      </label>
      <Show when={p.proto()._tag === "static"}>
        <label class="wash-net-field">
          <span class="wash-net-label">Address/CIDR</span>
          <input value={p.proto().IPAddr ?? ""} placeholder="192.168.1.1/24" onInput={(e) => p.setProto({ ...p.proto(), IPAddr: e.currentTarget.value })} />
        </label>
      </Show>
    </>
  );
}

function VLANWizard(props: { parents: string[]; onCancel: () => void; onCreate: (parent: string, vid: number, proto: Proto) => void }) {
  const [parent, setParent] = createSignal(props.parents[0] ?? "");
  const [vid, setVid] = createSignal(10);
  const [proto, setProto] = createSignal<Proto>({ _tag: "dhcp" });
  return (
    <div class="wash-net-wizard">
      <div class="wash-net-wizard-title">New VLAN</div>
      <label class="wash-net-field">
        <span class="wash-net-label">Parent</span>
        <select value={parent()} onChange={(e) => setParent(e.currentTarget.value)}>
          <For each={props.parents}>{(d) => <option value={d}>{d}</option>}</For>
        </select>
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">VLAN ID</span>
        <input type="number" min="1" max="4094" value={vid()} onInput={(e) => setVid(parseInt(e.currentTarget.value || "0", 10))} />
      </label>
      <div class="wash-net-field"><span class="wash-net-label">Device</span><span class="wash-net-derived">{parent()}.{vid()}</span></div>
      <AddressingFields proto={proto} setProto={setProto} />
      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button class="wash-net-btn primary" disabled={!parent() || vid() < 1 || vid() > 4094} onClick={() => props.onCreate(parent(), vid(), proto())}>Create</button>
      </div>
    </div>
  );
}

function BridgeWizard(props: { members: string[]; onCancel: () => void; onCreate: (name: string, members: string[], proto: Proto) => void }) {
  const [name, setName] = createSignal("br0");
  const [picked, setPicked] = createSignal<Set<string>>(new Set());
  const [proto, setProto] = createSignal<Proto>({ _tag: "dhcp" });
  const toggle = (d: string) => setPicked((s) => { const n = new Set(s); n.has(d) ? n.delete(d) : n.add(d); return n; });
  return (
    <div class="wash-net-wizard">
      <div class="wash-net-wizard-title">New Bridge</div>
      <label class="wash-net-field">
        <span class="wash-net-label">Name</span>
        <input value={name()} onInput={(e) => setName(e.currentTarget.value)} />
      </label>
      <div class="wash-net-field">
        <span class="wash-net-label">Members</span>
        <div class="wash-net-members">
          <For each={props.members} fallback={<span class="wash-net-hint">no free interfaces</span>}>
            {(d) => <label class="wash-net-member"><input type="checkbox" checked={picked().has(d)} onChange={() => toggle(d)} /> {d}</label>}
          </For>
        </div>
      </div>
      <AddressingFields proto={proto} setProto={setProto} />
      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button class="wash-net-btn primary" disabled={!name() || picked().size === 0} onClick={() => props.onCreate(name(), Array.from(picked()), proto())}>Create</button>
      </div>
    </div>
  );
}

const STYLE = `
.wash-net-app { display:flex; flex-direction:column; height:100%; }
.wash-net-head { display:flex; align-items:center; justify-content:space-between; padding:8px 12px; border-bottom:1px solid #2a2d33; }
.wash-net-head h1 { font-size:14px; font-weight:600; margin:0; }
.wash-net-add { display:flex; gap:6px; }
.wash-net-body { flex:1; overflow:auto; padding:10px 12px; }
.wash-net-list { display:flex; flex-direction:column; gap:6px; }
.wash-net-empty { opacity:.6; font-size:12px; padding:16px; text-align:center; }
.wash-net-conn { display:grid; grid-template-columns:1fr auto; grid-template-rows:auto auto; gap:2px 8px;
                 border:1px solid #2a2d33; border-radius:6px; padding:8px 10px; align-items:center; }
.wash-net-conn-main { display:flex; align-items:baseline; gap:8px; }
.wash-net-conn-name { font-weight:600; font-size:13px; }
.wash-net-conn-kind { font-size:10px; text-transform:uppercase; letter-spacing:.04em; color:#8fb0e0; border:1px solid #33415a; border-radius:9px; padding:1px 7px; }
.wash-net-conn-dev { font-size:11px; opacity:.6; font-family:ui-monospace,Menlo,monospace; }
.wash-net-conn-sub { grid-column:1; font-size:11px; opacity:.7; }
.wash-net-conn .ghost { grid-row:1 / span 2; grid-column:2; }
.wash-net-greyed { display:flex; gap:10px; align-items:center; margin-top:14px; padding-top:10px; border-top:1px dashed #2a2d33; }
.wash-net-lock { font-size:12px; opacity:.45; }
.wash-net-hint { font-size:11px; opacity:.4; font-style:italic; }
.wash-net-wizard { border:1px solid #3a4a6a; border-radius:8px; padding:10px 12px; margin-bottom:12px; background:#1a1d24; }
.wash-net-wizard-title { font-size:13px; font-weight:600; margin-bottom:8px; }
.wash-net-wizard-actions { display:flex; justify-content:flex-end; gap:8px; margin-top:10px; }
.wash-net-field { display:grid; grid-template-columns:130px 1fr; align-items:center; gap:8px; margin:5px 0; }
.wash-net-label { font-size:12px; opacity:.85; }
.wash-net-derived { font-size:12px; font-family:ui-monospace,Menlo,monospace; opacity:.8; }
.wash-net-field input, .wash-net-field select { background:#1b1d22; color:#cfd0d4; border:1px solid #33363d; border-radius:4px; padding:3px 6px; font:inherit; }
.wash-net-members { display:flex; flex-wrap:wrap; gap:10px; }
.wash-net-member { font-size:12px; display:flex; gap:4px; align-items:center; }
.wash-net-btn { background:#23252b; color:#ddd; border:1px solid #3a3a4a; border-radius:4px; padding:4px 12px; font:inherit; cursor:pointer; }
.wash-net-btn.primary { background:#3a5a9a; border-color:#3a5a9a; color:#fff; }
.wash-net-btn.ghost { background:transparent; opacity:.7; }
.wash-net-btn:disabled { opacity:.4; cursor:default; }

/* --- apply terminal (B3) --- */
.wash-net-apply { border-top:1px solid #2a2d33; display:flex; flex-direction:column; flex-shrink:0; }
.wash-net-rail { display:flex; align-items:center; gap:6px; padding:8px 12px 4px; }
.wash-net-chip { font-size:10px; text-transform:uppercase; letter-spacing:.04em; padding:2px 8px; border-radius:10px; border:1px solid #33363d; color:#7a7d85; }
.wash-net-chip[data-state="done"] { color:#3aa050; border-color:#2e5a38; }
.wash-net-chip[data-state="active"] { color:#d0a040; border-color:#4a4030; animation:wash-pulse 1.2s ease-in-out infinite; }
.wash-net-chip[data-state="bad"] { color:#e06060; border-color:#5a2e2e; }
@keyframes wash-pulse { 0%,100% { opacity:1; } 50% { opacity:.5; } }
.wash-net-log { max-height:120px; overflow:auto; margin:0 12px; padding:4px 0; font:11px ui-monospace, Menlo, Consolas, monospace; }
.wash-net-logline { display:flex; gap:8px; padding:1px 0; }
.wash-net-logphase { color:#6a6d75; min-width:80px; text-transform:uppercase; }
.wash-net-logline[data-level="warn"] .wash-net-logmsg { color:#d0a040; }
.wash-net-logline[data-level="error"] .wash-net-logmsg { color:#e06060; }
.wash-net-applybar, .wash-net-confirm { display:flex; align-items:center; gap:8px; padding:8px 12px; }
.wash-net-status { flex:1; font-size:12px; opacity:.8; font-variant:tabular-nums; }
.wash-net-countdown { flex:1; position:relative; height:22px; border-radius:4px; overflow:hidden; background:#1b1d22; border:1px solid #4a4030; display:flex; align-items:center; }
.wash-net-countbar { position:absolute; inset:0 auto 0 0; background:rgba(208,160,64,.18); transition:width .25s linear; }
.wash-net-counttext { position:relative; padding:0 8px; font-size:11px; color:#e8c878; }
`;

defineWashApp("wash-app-net", NetApp, { style: "display:block;height:100%;overflow:hidden;font:13px/1.5 ui-sans-serif,system-ui,sans-serif;color:#cfd0d4;" });

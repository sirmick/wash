// wash-app-net — the windowed network UI (docs/NET.md §2.11, §6, B5).
//
// The type-2 (workstation) settings app: a Connections list loaded from the
// box's live state (netd `current`, which reads real NetworkManager), an
// `+ Add` menu of recipe-style wizards (VLAN, Bridge, …) gated by the backend's
// capabilities, and the commit-confirm apply terminal. Adding a VLAN or bridging
// NICs is a multi-object change (a Device + an Interface, members become ports),
// so those are wizards, not single-object edits; the generic <ObjectForm> is
// the schema-driven editor for one interface's addressing. Every change runs through
// netd validate → apply (commit-confirm) → the box.

import { createMemo, createSignal, onCleanup, onMount, For, Show } from "solid-js";
import { defineWashApp, type WashAppProps } from "@wash/ui";

import { ApplyTerminal, type ApplyEvent } from "./ApplyTerminal.tsx";
import { ObjectForm } from "./ObjectForm.tsx";
import type { Descriptor, ObjectDescriptor } from "./objectform-model.ts";
import descriptorJson from "./generated/descriptor.json";
import i18nJson from "./generated/i18n.json";

const desc = descriptorJson as unknown as Descriptor;
const i18n = i18nJson as Record<string, string>;
const label = (k: string) => i18n[k] ?? k.split(".").pop() ?? k;
const ifaceDesc = desc.objects.find((o) => o.kind === "network/interface")!;
// A proto-only slice of the Interface descriptor: the wizards are bespoke
// containers (NIC picker / VID / members), but the ADDRESSING they all share is
// rendered straight from the schema — so the fields and what's optional
// (gateway, DNS) come from the one model, not hand-maintained here.
//
// Only the genuine IP-addressing methods are offered here. PPPoE and WireGuard
// are connection TYPES, not ways to address an ethernet/vlan/bridge, so they get
// their own connection flow rather than appearing in this method dropdown.
const ADDRESSING_METHODS = new Set(["dhcp", "static", "none"]);
const protoField = ifaceDesc.fields.find((f) => f.name === "Proto")!;
const addressingDesc: ObjectDescriptor = {
  ...ifaceDesc,
  fields: [protoField.union
    ? { ...protoField, union: { ...protoField.union, variants: protoField.union.variants.filter((v) => ADDRESSING_METHODS.has(v.tag)) } }
    : protoField],
};

// setAtPath immutably sets a dotted path; a union switch arrives as the bare
// discriminator path with a {_tag} value, replacing the variant (dropping the
// old variant's fields).
function setAtPath(obj: Record<string, any>, keys: string[], v: unknown): Record<string, any> {
  const [head, ...rest] = keys;
  const next = { ...obj };
  next[head] = rest.length === 0 ? v : setAtPath((obj[head] as Record<string, any>) ?? {}, rest, v);
  return next;
}

type Proto = {
  _tag: string;
  IPAddr?: string; Gateway?: string; IP6Addr?: string; IP6Gw?: string; DNS?: string[];
  Hostname?: string; IPv4?: boolean; IPv6?: boolean;
};
// DHCP defaults to requesting both families; picking the method seeds these so
// the v4/v6 checkboxes render on.
const dhcpDefault = (): Proto => ({ _tag: "dhcp", IPv4: true, IPv6: true });
type Interface = { Name: string; Device?: string; Proto?: Proto };
type Device = { Name: string; Type?: string; Ports?: string[]; Ifname?: string; VID?: number };
type Config = { Interfaces?: Interface[]; Devices?: Device[]; [k: string]: any };
// Caps mirrors netd's generic capability wire (docs/NET.md §2.7): the supported
// feature keys + object-kind keys. The UI greys by set membership, so a new
// backend (networkd, uci) advertising a different subset re-gates without any
// per-feature field here. `can(f)` / `kind(k)` are the membership helpers.
type Caps = { features: Set<string>; kinds: Set<string> };
const emptyCaps = (): Caps => ({ features: new Set(), kinds: new Set() });
const toCaps = (raw: any): Caps => ({
  features: new Set<string>(Array.isArray(raw?.features) ? raw.features : []),
  kinds: new Set<string>(Array.isArray(raw?.kinds) ? raw.kinds : []),
});

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

// Inline lucide icons (the shared chrome sprite is lucide too, but carries none
// of the networking glyphs we need). Each entry is the icon's inner SVG markup.
const ICONS: Record<string, string> = {
  plus: '<path d="M5 12h14"/><path d="M12 5v14"/>',
  ethernet: '<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>',
  "git-branch": '<line x1="6" x2="6" y1="3" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>',
  "git-merge": '<circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M6 21V9a9 9 0 0 0 9 9"/>',
  trash: '<path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>',
  pencil: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
  check: '<path d="M20 6 9 17l-5-5"/>',
  x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  undo: '<path d="M3 7v6h6"/><path d="M21 17a9 9 0 0 0-9-9 9 9 0 0 0-6 2.3L3 13"/>',
};
function Icon(p: { name: keyof typeof ICONS | string; size?: number }) {
  return (
    <svg class="wash-net-ico" width={p.size ?? 14} height={p.size ?? 14} viewBox="0 0 24 24"
      fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
      innerHTML={ICONS[p.name] ?? ""} />
  );
}

function NetApp(props: WashAppProps) {
  const [config, setConfig] = createSignal<Config>({ Interfaces: [], Devices: [] }); // committed baseline (from netd)
  const [draft, setDraft] = createSignal<Config>({ Interfaces: [], Devices: [] });  // staged edits, not yet applied
  const [caps, setCaps] = createSignal<Caps>(emptyCaps());
  const can = (f: string) => caps().features.has(f);
  const [links, setLinks] = createSignal<string[]>([]); // physical NICs from the backend
  const [adding, setAdding] = createSignal<null | "ethernet" | "vlan" | "bridge">(null);
  const [editIface, setEditIface] = createSignal<Interface | null>(null);

  const [status, setStatus] = createSignal("idle");
  const [events, setEvents] = createSignal<ApplyEvent[]>([]);
  const [confirmWindowMs, setConfirmWindowMs] = createSignal(0);
  const [deadline, setDeadline] = createSignal(0);
  const [remaining, setRemaining] = createSignal(0);
  const [busy, setBusy] = createSignal(false);

  // Everything the list + wizards show reflects the DRAFT (staged edits), not the
  // committed baseline — so a staged bridge immediately claims its members, etc.
  const devByName = createMemo(() => {
    const m = new Map<string, Device>();
    for (const d of draft().Devices ?? []) m.set(d.Name, d);
    return m;
  });
  // Physical NICs available as VLAN parents / bridge members: the backend's
  // managed links, minus any already consumed by a bridge/vlan or carrying a
  // standalone interface (in the draft).
  const freeDevices = createMemo(() => {
    const taken = new Set<string>();
    for (const d of draft().Devices ?? []) {
      (d.Ports ?? []).forEach((p) => taken.add(p));
      if (d.Ifname) taken.add(d.Ifname);
    }
    for (const i of draft().Interfaces ?? []) {
      if (i.Device) taken.add(i.Device);
    }
    return links().filter((d) => !taken.has(d));
  });
  // VLAN parents are broader than "free" NICs: a vlan can ride ANY L2 device that
  // keeps its own identity — every physical NIC that isn't enslaved as a bridge
  // port (even one already carrying a base IP or other vlans), plus bridges. So
  // you can stack eth0.10 + eth0.20 on one NIC, or put a vlan on a bridge.
  const vlanParents = createMemo(() => {
    const ports = new Set<string>();
    for (const d of draft().Devices ?? []) (d.Ports ?? []).forEach((p) => ports.add(p));
    const out = links().filter((l) => !ports.has(l));
    for (const d of draft().Devices ?? []) if (d.Type === "bridge") out.push(d.Name);
    return out;
  });
  // Physical adapters with no connection yet — listed so the box's full hardware
  // is visible (not just configured interfaces), each with a Configure shortcut.
  const unconfiguredLinks = createMemo(() => {
    const used = new Set<string>();
    for (const i of draft().Interfaces ?? []) if (i.Device) used.add(i.Device);
    for (const d of draft().Devices ?? []) {
      used.add(d.Name);
      if (d.Ifname) used.add(d.Ifname);
      (d.Ports ?? []).forEach((p) => used.add(p));
    }
    return links().filter((l) => !used.has(l));
  });
  // configureDevice pre-selects a NIC when the Ethernet wizard is opened from an
  // unconfigured adapter's Configure button ("" = normal Add Ethernet).
  const [configureDevice, setConfigureDevice] = createSignal<string>("");

  // --- draft vs committed diff (what the UI badges as new/edited/removed) ----
  // A connection's signature is its interface object plus its backing device
  // (bridge ports / vlan vid live there), so editing either flags it.
  const sigOf = (cfg: Config, iface: Interface): string => {
    const d = (cfg.Devices ?? []).find((x) => x.Name === iface.Device);
    return JSON.stringify([iface, d ?? null]);
  };
  const committedSig = createMemo(() => {
    const m = new Map<string, string>();
    for (const i of config().Interfaces ?? []) m.set(i.Name, sigOf(config(), i));
    return m;
  });
  const statusOf = (iface: Interface): "new" | "edited" | "clean" => {
    const c = committedSig().get(iface.Name);
    if (c === undefined) return "new";
    return sigOf(draft(), iface) === c ? "clean" : "edited";
  };
  // Committed connections the draft dropped — shown as ghost rows so the pending
  // removal is visible (and undoable) before it's applied.
  const removed = createMemo(() => {
    const have = new Set((draft().Interfaces ?? []).map((i) => i.Name));
    return (config().Interfaces ?? []).filter((i) => !have.has(i.Name));
  });
  const dirtyCount = createMemo(() => {
    let n = removed().length;
    for (const i of draft().Interfaces ?? []) if (statusOf(i) !== "clean") n++;
    return n;
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
      const cfg = (r.config ?? { Interfaces: [], Devices: [] }) as Config;
      setConfig(cfg);
      setDraft(structuredClone(cfg)); // reset the draft to the freshly committed state
      setCaps(toCaps(r.caps));
      setLinks((r.devices ?? []) as string[]);
    }
  };

  // applyDraft is the EXPLICIT apply: it ships the whole staged draft through the
  // commit-confirm transaction. Nothing reaches the box until the user hits Apply.
  const applyDraft = async () => {
    setBusy(true);
    setStatus("applying"); setEvents([]);
    const r = await sendWithReply("apply", { config: draft() }, 30000);
    setBusy(false);
    if (r.kind === "apply_ok") {
      applyState({ status: r.state, events: r.events, diagnostics: r.diagnostics, confirm_window_ms: r.confirm_window_ms });
    } else {
      setStatus(`error: ${r.msg ?? r.code}`);
    }
  };
  // Discard all staged edits locally (no backend round-trip) — back to committed.
  const discardDraft = () => { setStatus("idle"); setEvents([]); setDraft(structuredClone(config())); };

  const finish = (kind: "confirm" | "revert") => async () => {
    const r = await sendWithReply(kind, {}, 30000);
    if (r.kind === `${kind}_ok`) setStatus(r.state ?? "");
    else setStatus(`error: ${r.msg ?? r.code}`);
    await loadCurrent(); // refresh from the (now committed/reverted) box; resets the draft
  };

  // --- draft mutations (stage only; Apply commits) -------------------------
  const removeConnection = (iface: Interface) => {
    setDraft((d) => {
      const next = structuredClone(d);
      next.Interfaces = (next.Interfaces ?? []).filter((i) => i.Name !== iface.Name);
      if (iface.Device) next.Devices = (next.Devices ?? []).filter((dev) => dev.Name !== iface.Device);
      return next;
    });
  };
  // Undo a staged removal: restore the connection (+ its device) from committed.
  const undoRemove = (iface: Interface) => {
    setDraft((d) => {
      const next = structuredClone(d);
      next.Interfaces = [...(next.Interfaces ?? []), structuredClone(iface)];
      const dev = (config().Devices ?? []).find((x) => x.Name === iface.Device);
      if (dev && !(next.Devices ?? []).some((x) => x.Name === dev.Name)) {
        next.Devices = [...(next.Devices ?? []), structuredClone(dev)];
      }
      return next;
    });
  };

  const saveEthernet = (name: string, device: string, proto: Proto) => {
    setDraft((d) => {
      const next = structuredClone(d);
      const iface: Interface = { Name: name, Device: device, Proto: proto };
      const ifaces = next.Interfaces ?? [];
      const at = ifaces.findIndex((i) => i.Name === name || i.Device === device);
      if (at >= 0) ifaces[at] = iface; else ifaces.push(iface);
      next.Interfaces = ifaces;
      return next;
    });
    setAdding(null); setEditIface(null);
  };
  const addVLAN = (parent: string, vid: number, proto: Proto) => {
    const dev = `${parent}.${vid}`;
    setDraft((d) => {
      const next = structuredClone(d);
      next.Devices = [...(next.Devices ?? []), { Name: dev, Type: "8021q", Ifname: parent, VID: vid }];
      next.Interfaces = [...(next.Interfaces ?? []), { Name: `${parent}_${vid}`, Device: dev, Proto: proto }];
      return next;
    });
    setAdding(null);
  };
  const addBridge = (name: string, members: string[], proto: Proto) => {
    setDraft((d) => {
      const next = structuredClone(d);
      const isMember = new Set(members);
      next.Interfaces = (next.Interfaces ?? []).filter((i) => !isMember.has(i.Device ?? ""));
      next.Devices = [...(next.Devices ?? []), { Name: name, Type: "bridge", Ports: members }];
      next.Interfaces = [...next.Interfaces, { Name: name, Device: name, Proto: proto }];
      return next;
    });
    setAdding(null);
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
        <div class="wash-net-add">
          <button data-testid="add-ethernet" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy()} onClick={() => { setConfigureDevice(""); setAdding("ethernet"); }}><Icon name="ethernet" /> Ethernet</button>
          <button data-testid="add-vlan" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy() || !can("vlan")} onClick={() => setAdding("vlan")}><Icon name="git-branch" /> VLAN</button>
          <button data-testid="add-bridge" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy() || !can("bridge")} onClick={() => setAdding("bridge")}><Icon name="git-merge" /> Bridge</button>
        </div>
      </header>

      <div class="wash-net-body">
        <Show when={adding() === "ethernet" || editIface()}>
          <EthernetWizard
            nics={editIface() ? [] : links()}
            defaultDevice={configureDevice()}
            initial={editIface() ?? undefined}
            onCancel={() => { setAdding(null); setEditIface(null); setConfigureDevice(""); }}
            onSave={saveEthernet}
          />
        </Show>
        <Show when={adding() === "vlan"}>
          <VLANWizard parents={vlanParents()} onCancel={() => setAdding(null)} onCreate={addVLAN} />
        </Show>
        <Show when={adding() === "bridge"}>
          <BridgeWizard members={freeDevices()} onCancel={() => setAdding(null)} onCreate={addBridge} />
        </Show>

        <div class="wash-net-list">
          <For each={draft().Interfaces ?? []} fallback={<Show when={removed().length === 0}><div class="wash-net-empty">No connections yet — use + Ethernet / + VLAN / + Bridge.</div></Show>}>
            {(iface) => {
              const d = devByName().get(iface.Device ?? "");
              const st = () => statusOf(iface);
              return (
                <div class="wash-net-conn" data-testid={`conn-${iface.Name}`} data-kind={kindOf(iface)} data-device={iface.Device ?? ""} data-status={st()}>
                  <div class="wash-net-conn-main">
                    <span class="wash-net-conn-name">{iface.Name}</span>
                    <span class="wash-net-conn-kind">{kindOf(iface)}</span>
                    <span class="wash-net-conn-dev">{iface.Device ?? "—"}</span>
                    <Show when={st() !== "clean"}><span class="wash-net-badge" data-badge={st()}>{st()}</span></Show>
                  </div>
                  <div class="wash-net-conn-sub">
                    {protoLabel(iface.Proto)}
                    <Show when={d?.Type === "bridge"}> · ports: {(d?.Ports ?? []).join(", ")}</Show>
                  </div>
                  <div class="wash-net-conn-actions">
                    <Show when={kindOf(iface) === "Ethernet"}>
                      <button class="wash-net-btn ghost" title="Edit" disabled={busy() || adding() !== null} onClick={() => setEditIface(iface)}><Icon name="pencil" /> Edit</button>
                    </Show>
                    <button class="wash-net-btn ghost" title="Remove" disabled={busy()} onClick={() => removeConnection(iface)}><Icon name="trash" /> Remove</button>
                  </div>
                </div>
              );
            }}
          </For>
          <For each={unconfiguredLinks()}>
            {(link) => (
              <div class="wash-net-conn" data-testid={`adapter-${link}`} data-kind="Adapter" data-device={link} data-status="unconfigured">
                <div class="wash-net-conn-main">
                  <span class="wash-net-conn-name">{link}</span>
                  <span class="wash-net-conn-kind">Adapter</span>
                  <span class="wash-net-conn-dev">{link}</span>
                  <span class="wash-net-badge" data-badge="unconfigured">unconfigured</span>
                </div>
                <div class="wash-net-conn-actions">
                  <button class="wash-net-btn ghost" title="Configure this adapter" disabled={busy() || adding() !== null || editIface() !== null} onClick={() => { setConfigureDevice(link); setAdding("ethernet"); }}><Icon name="plus" /> Configure</button>
                </div>
              </div>
            )}
          </For>
          <For each={removed()}>
            {(iface) => (
              <div class="wash-net-conn removed" data-testid={`conn-removed-${iface.Name}`} data-status="removed">
                <div class="wash-net-conn-main">
                  <span class="wash-net-conn-name">{iface.Name}</span>
                  <span class="wash-net-badge" data-badge="removed">removed</span>
                </div>
                <div class="wash-net-conn-actions">
                  <button class="wash-net-btn ghost" disabled={busy()} onClick={() => undoRemove(iface)}><Icon name="undo" /> Undo</button>
                </div>
              </div>
            )}
          </For>
        </div>

        <div class="wash-net-greyed">
          <Show when={!can("zones")}><span class="wash-net-lock">Firewall 🔒</span></Show>
          <Show when={!can("dhcp-server")}><span class="wash-net-lock">DHCP server 🔒</span></Show>
          <Show when={!can("ap")}><span class="wash-net-lock">Access point 🔒</span></Show>
          <span class="wash-net-hint">available in router mode</span>
        </div>
      </div>

      <Show when={dirtyCount() > 0 && status() !== "applying" && status() !== "await-confirm"}>
        <div class="wash-net-pending" data-testid="pending-bar">
          <span class="wash-net-pending-msg">
            {dirtyCount()} pending change{dirtyCount() === 1 ? "" : "s"} — not applied yet
          </span>
          <button class="wash-net-btn" data-testid="discard-changes" disabled={busy()} onClick={discardDraft}><Icon name="x" /> Discard</button>
          <button class="wash-net-btn primary" data-testid="apply-button" disabled={busy()} onClick={() => void applyDraft()}><Icon name="check" /> Apply</button>
        </div>
      </Show>

      <ApplyTerminal
        status={status}
        events={events}
        remainingMs={remaining}
        windowMs={confirmWindowMs}
        onKeep={finish("confirm")}
        onDiscard={finish("revert")}
      />
    </div>
  );
}

// --- wizards ---------------------------------------------------------------

// addrLabel humanizes the proto field + its variant tags for the addressing
// fragment. Field STRUCTURE comes from the schema; these are just presentation
// (i18n) — the model speaks UCI's "proto"/"static"; users want "IPv4 method"/
// "Manual". Unknown keys fall back to the generated i18n.
const PROTO_LABELS: Record<string, string> = {
  "Interface.Proto": "IP method",
  "variant.dhcp": "Automatic (DHCP)",
  "variant.static": "Manual (static IP)",
  "variant.none": "Disabled (no IP)",
  "variant.pppoe": "PPPoE",
  "variant.wireguard": "WireGuard",
  // Friendly field labels for the addressing fragment.
  "StaticProto.IPAddr": "IPv4 address",
  "StaticProto.Gateway": "IPv4 gateway",
  "StaticProto.IP6Addr": "IPv6 address",
  "StaticProto.IP6Gw": "IPv6 gateway",
  "StaticProto.DNS": "DNS servers",
  "DHCPProto.IPv4": "IPv4 (DHCP)",
  "DHCPProto.IPv6": "IPv6 (DHCP / SLAAC)",
  "DHCPProto.Hostname": "DHCP hostname",
};
const addrLabel = (k: string) => PROTO_LABELS[k] ?? label(k);

// AddressingFields renders the interface proto union (IPv4 method + its fields)
// from the schema — every field always shows; gateway/DNS are simply optional —
// instead of hand-coded inputs, so it tracks the model and its validation.
function AddressingFields(p: { proto: () => Proto; setProto: (x: Proto) => void }) {
  const value = () => ({ Proto: p.proto() });
  const onChange = (path: string, v: unknown) => {
    // Switching the method to DHCP seeds both-family defaults so its checkboxes
    // render on (a bare {_tag:"dhcp"} would show them unchecked).
    if (path === "Proto" && (v as any)?._tag === "dhcp") v = dhcpDefault();
    // buildForm emits "Proto" / "Proto.IPAddr" (no leading dot) for pathPrefix="".
    const next = setAtPath(value(), path.split("."), v);
    p.setProto((next.Proto ?? dhcpDefault()) as Proto);
  };
  return (
    <div class="wash-net-addressing" data-testid="addressing">
      <ObjectForm object={addressingDesc} value={value()} pathPrefix="" label={addrLabel} onChange={onChange} />
    </div>
  );
}

function EthernetWizard(props: { nics: string[]; defaultDevice?: string; initial?: Interface; onCancel: () => void; onSave: (name: string, device: string, proto: Proto) => void }) {
  const editing = !!props.initial;
  const seed = props.initial?.Device ?? (props.defaultDevice || props.nics[0]) ?? "";
  const [device, setDevice] = createSignal(seed);
  const [name, setName] = createSignal(props.initial?.Name ?? seed);
  const [proto, setProto] = createSignal<Proto>(props.initial?.Proto ?? dhcpDefault());
  // When adding, the connection name follows the chosen NIC unless edited.
  let nameTouched = editing;
  return (
    <div class="wash-net-wizard" data-testid="eth-wizard">
      <div class="wash-net-wizard-title">{editing ? `Edit ${props.initial!.Name}` : "New Ethernet connection"}</div>
      <Show when={!editing} fallback={<div class="wash-net-field"><span class="wash-net-label">Interface</span><span class="wash-net-derived">{device()}</span></div>}>
        <label class="wash-net-field">
          <span class="wash-net-label">Interface</span>
          <select value={device()} onChange={(e) => { setDevice(e.currentTarget.value); if (!nameTouched) setName(e.currentTarget.value); }}>
            <For each={props.nics}>{(d) => <option value={d}>{d}</option>}</For>
          </select>
        </label>
      </Show>
      <label class="wash-net-field">
        <span class="wash-net-label">Name</span>
        <input value={name()} onInput={(e) => { nameTouched = true; setName(e.currentTarget.value); }} />
      </label>
      <AddressingFields proto={proto} setProto={setProto} />
      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button data-testid="eth-create" class="wash-net-btn primary" disabled={!name() || !device()} onClick={() => props.onSave(name(), device(), proto())}>{editing ? "Save" : "Create"}</button>
      </div>
    </div>
  );
}

function VLANWizard(props: { parents: string[]; onCancel: () => void; onCreate: (parent: string, vid: number, proto: Proto) => void }) {
  const [parent, setParent] = createSignal(props.parents[0] ?? "");
  const [vid, setVid] = createSignal(10);
  const [proto, setProto] = createSignal<Proto>(dhcpDefault());
  return (
    <div class="wash-net-wizard" data-testid="vlan-wizard">
      <div class="wash-net-wizard-title">New VLAN</div>
      <label class="wash-net-field">
        <span class="wash-net-label">Parent</span>
        <select data-testid="vlan-parent" value={parent()} onChange={(e) => setParent(e.currentTarget.value)}>
          <For each={props.parents}>{(d) => <option value={d}>{d}</option>}</For>
        </select>
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">VLAN ID</span>
        <input data-testid="vlan-vid" type="number" min="1" max="4094" value={vid()} onInput={(e) => setVid(parseInt(e.currentTarget.value || "0", 10))} />
      </label>
      <div class="wash-net-field"><span class="wash-net-label">Device</span><span class="wash-net-derived">{parent()}.{vid()}</span></div>
      <AddressingFields proto={proto} setProto={setProto} />
      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button data-testid="vlan-create" class="wash-net-btn primary" disabled={!parent() || vid() < 1 || vid() > 4094} onClick={() => props.onCreate(parent(), vid(), proto())}>Create</button>
      </div>
    </div>
  );
}

function BridgeWizard(props: { members: string[]; onCancel: () => void; onCreate: (name: string, members: string[], proto: Proto) => void }) {
  const [name, setName] = createSignal("br0");
  const [picked, setPicked] = createSignal<Set<string>>(new Set());
  const [proto, setProto] = createSignal<Proto>(dhcpDefault());
  const toggle = (d: string) => setPicked((s) => { const n = new Set(s); n.has(d) ? n.delete(d) : n.add(d); return n; });
  return (
    <div class="wash-net-wizard" data-testid="bridge-wizard">
      <div class="wash-net-wizard-title">New Bridge</div>
      <label class="wash-net-field">
        <span class="wash-net-label">Name</span>
        <input data-testid="bridge-name" value={name()} onInput={(e) => setName(e.currentTarget.value)} />
      </label>
      <div class="wash-net-field">
        <span class="wash-net-label">Members</span>
        <div class="wash-net-members">
          <For each={props.members} fallback={<span class="wash-net-hint">no free interfaces</span>}>
            {(d) => <label class="wash-net-member"><input data-testid={`member-${d}`} type="checkbox" checked={picked().has(d)} onChange={() => toggle(d)} /> {d}</label>}
          </For>
        </div>
      </div>
      <AddressingFields proto={proto} setProto={setProto} />
      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button data-testid="bridge-create" class="wash-net-btn primary" disabled={!name() || picked().size === 0} onClick={() => props.onCreate(name(), Array.from(picked()), proto())}>Create</button>
      </div>
    </div>
  );
}

const STYLE = `
.wash-net-app { display:flex; flex-direction:column; height:100%; background:#181828; color:#eee;
  font:13px ui-sans-serif, system-ui, sans-serif; }
.wash-net-head { display:flex; align-items:center; justify-content:space-between; padding:8px 12px; border-bottom:1px solid #2a2a3a; }
.wash-net-head h1 { font-size:14px; font-weight:600; margin:0; }
.wash-net-add { display:flex; gap:6px; }
.wash-net-body { flex:1; overflow:auto; padding:10px 12px; }
.wash-net-list { display:flex; flex-direction:column; gap:6px; }
.wash-net-empty { opacity:.6; font-size:12px; padding:16px; text-align:center; }
.wash-net-conn { display:grid; grid-template-columns:1fr auto; grid-template-rows:auto auto; gap:2px 8px;
                 border:1px solid #2a2a3a; border-radius:6px; padding:8px 10px; align-items:center; }
.wash-net-conn-main { display:flex; align-items:baseline; gap:8px; }
.wash-net-conn-name { font-weight:600; font-size:13px; }
.wash-net-conn-kind { font-size:10px; text-transform:uppercase; letter-spacing:.04em; color:#8fb0e0; border:1px solid #33415a; border-radius:9px; padding:1px 7px; }
.wash-net-conn-dev { font-size:11px; opacity:.6; font-family:ui-monospace,Menlo,monospace; }
.wash-net-conn-sub { grid-column:1; font-size:11px; opacity:.7; }
.wash-net-conn-actions { grid-row:1 / span 2; grid-column:2; display:flex; gap:4px; align-items:center; }
.wash-net-conn[data-status="new"] { border-color:#2e5a38; }
.wash-net-conn[data-status="edited"] { border-color:#4a4030; }
.wash-net-conn.removed { opacity:.6; border-style:dashed; }
.wash-net-conn.removed .wash-net-conn-name { text-decoration:line-through; }
.wash-net-badge { font-size:9px; text-transform:uppercase; letter-spacing:.05em; border-radius:8px; padding:1px 6px; border:1px solid; }
.wash-net-badge[data-badge="new"] { color:#3aa050; border-color:#2e5a38; }
.wash-net-badge[data-badge="edited"] { color:#d0a040; border-color:#4a4030; }
.wash-net-badge[data-badge="removed"] { color:#e06060; border-color:#5a2e2e; }
.wash-net-pending { display:flex; align-items:center; gap:10px; padding:8px 12px; border-top:1px solid #2a2a3a;
  background:#1a1a30; flex-shrink:0; }
.wash-net-pending-msg { flex:1; font-size:12px; color:#d0a040; }
.wash-net-greyed { display:flex; gap:10px; align-items:center; margin-top:14px; padding-top:10px; border-top:1px dashed #2a2a3a; }
.wash-net-lock { font-size:12px; opacity:.45; }
.wash-net-hint { font-size:11px; opacity:.4; font-style:italic; }
.wash-net-wizard { border:1px solid #3a3a6a; border-radius:8px; padding:10px 12px; margin-bottom:12px; background:#15152a; }
.wash-net-wizard-title { font-size:13px; font-weight:600; margin-bottom:8px; }
.wash-net-wizard-actions { display:flex; justify-content:flex-end; gap:8px; margin-top:10px; }
.wash-net-field { display:grid; grid-template-columns:130px 1fr; align-items:center; gap:8px; margin:5px 0; }
.wash-net-label { font-size:12px; opacity:.85; }
.wash-net-derived { font-size:12px; font-family:ui-monospace,Menlo,monospace; opacity:.8; }
.wash-net-field input, .wash-net-field select, .wash-net-field textarea {
  background:#15152a; color:#eee; border:1px solid #2a2a3a; border-radius:4px; padding:3px 6px; font:inherit; }
.wash-net-field input:focus, .wash-net-field select:focus, .wash-net-field textarea:focus {
  outline:none; border-color:#3a3a6a; }
.wash-net-field textarea { resize:vertical; font:12px ui-monospace, Menlo, Consolas, monospace; }
.wash-net-reflist { display:flex; flex-wrap:wrap; gap:8px; }
.wash-net-reflist label { display:flex; gap:4px; align-items:center; font-size:12px; }
.wash-net-diag { grid-column:2; font-size:11px; color:#d0a040; }
.wash-net-field.error input, .wash-net-field.error select, .wash-net-field.error textarea { border-color:#a02d2d; }
.wash-net-field.error .wash-net-diag { color:#e06060; }
.wash-net-members { display:flex; flex-wrap:wrap; gap:10px; }
.wash-net-addressing { margin-top:6px; }
.wash-net-addressing .wash-net-form { padding:0; }
.wash-net-group { border:none; padding:0; margin:0; }
.wash-net-method { margin:5px 0; }
.wash-net-method select { width:100%; background:#15152a; color:#eee; border:1px solid #2a2a3a; border-radius:4px; padding:4px 6px; font:inherit; }
.wash-net-method select:focus { outline:none; border-color:#3a3a6a; }
.wash-net-grouplabel { font-size:11px; opacity:.6; text-transform:uppercase; letter-spacing:.04em; margin:6px 0 2px; }
.wash-net-addressing .wash-net-group { margin:0 0 6px; }
.wash-net-member { font-size:12px; display:flex; gap:4px; align-items:center; }
.wash-net-btn { display:inline-flex; align-items:center; gap:5px; background:#202037; color:#eee; border:1px solid #2a2a3a; border-radius:4px; padding:4px 10px; font:inherit; cursor:pointer; }
.wash-net-ico { flex-shrink:0; opacity:.9; }
.wash-net-btn:hover:not(:disabled) { background:#2a2a4a; }
.wash-net-btn.primary { background:#3a5a9a; border-color:#3a5a9a; color:#fff; }
.wash-net-btn.primary:hover:not(:disabled) { background:#456bb5; }
.wash-net-btn.ghost { background:transparent; opacity:.7; }
.wash-net-btn:disabled { opacity:.4; cursor:default; }

/* --- apply terminal (B3) --- */
.wash-net-apply { border-top:1px solid #2a2a3a; display:flex; flex-direction:column; flex-shrink:0; }
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

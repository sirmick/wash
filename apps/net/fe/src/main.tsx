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

import { createEffect, createMemo, createSignal, onCleanup, onMount, For, Show } from "solid-js";
import { defineWashApp, tokens, washAssetUrl, type WashAppProps } from "@wash/ui";
import { x25519 } from "@noble/curves/ed25519.js";

import { ApplyTerminal, type ApplyEvent } from "./ApplyTerminal.tsx";
import { WifiDialog, type AP } from "./WifiDialog.tsx";
import { ObjectForm } from "./ObjectForm.tsx";
import { setAtPath } from "./setAtPath.ts";
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


type Proto = {
  _tag: string;
  IPAddr?: string; Gateway?: string; IP6Addr?: string; IP6Gw?: string; DNS?: string[];
  Hostname?: string; IPv4?: boolean; IPv6?: boolean;
  // wireguard variant: the local tunnel endpoint (the peer set lives in
  // Config.WGPeers, a separate kind keyed by this interface's Name).
  PrivateKey?: string; ListenPort?: number; Addresses?: string[];
};
// DHCP defaults to requesting both families; picking the method seeds these so
// the v4/v6 checkboxes render on.
const dhcpDefault = (): Proto => ({ _tag: "dhcp", IPv4: true, IPv6: true });

// genWGPrivateKey makes a Curve25519 private key the way `wg genkey` does: 32
// random bytes with the standard clamp, base64-encoded. We only generate the
// PRIVATE key — the backend (nmcli / networkd) derives our public key from it on
// apply, so no in-browser scalar-mult is needed.
const genWGPrivateKey = (): string => {
  const k = new Uint8Array(32);
  crypto.getRandomValues(k);
  k[0] &= 248; k[31] &= 127; k[31] |= 64;
  return btoa(String.fromCharCode(...k));
};
const b64ToBytes = (s: string): Uint8Array => Uint8Array.from(atob(s.trim()), (c) => c.charCodeAt(0));
// wgPublicKey derives the Curve25519 public key from a base64 private key the
// way `wg pubkey` does (scalar-mult against the base point, RFC-7748 clamping).
// Returns "" on any malformed key so the field just blanks out as you type.
const wgPublicKey = (privB64: string): string => {
  try {
    const priv = b64ToBytes(privB64);
    if (priv.length !== 32) return "";
    return btoa(String.fromCharCode(...x25519.getPublicKey(priv)));
  } catch { return ""; }
};

// PeerForm is the wizard's editable shape for one peer (strings throughout, so
// number fields can be empty). parseWgConf returns the same shape so an imported
// config drops straight in.
type PeerForm = { publicKey: string; presharedKey: string; endpointHost: string; endpointPort: string; allowedIPs: string; keepalive: string };
type WgImport = { privKey?: string; listenPort?: string; addresses?: string; dns?: string; peers: PeerForm[] };
const blankPeer = (): PeerForm => ({ publicKey: "", presharedKey: "", endpointHost: "", endpointPort: "", allowedIPs: "", keepalive: "" });

// splitEndpoint splits host:port, handling a bracketed IPv6 literal.
const splitEndpoint = (s: string): { host: string; port: string } => {
  const v6 = /^\[(.+)\]:(\d+)$/.exec(s);
  if (v6) return { host: v6[1], port: v6[2] };
  const i = s.lastIndexOf(":");
  return i >= 0 ? { host: s.slice(0, i), port: s.slice(i + 1) } : { host: s, port: "" };
};

// parseWgConf reads a standard wg-quick config — the exact text a WireGuard QR
// code encodes: one [Interface] section plus one or more [Peer] sections. Keys
// match case-insensitively; comments (# or ;) and unknown keys are ignored.
const parseWgConf = (text: string): WgImport => {
  const out: WgImport = { peers: [] };
  let section = "";
  let peer: PeerForm | null = null;
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.replace(/[#;].*$/, "").trim();
    if (!line) continue;
    const sec = /^\[(\w+)\]$/.exec(line);
    if (sec) {
      section = sec[1].toLowerCase();
      if (section === "peer") { peer = blankPeer(); out.peers.push(peer); }
      continue;
    }
    const kv = /^([A-Za-z]+)\s*=\s*(.*)$/.exec(line);
    if (!kv) continue;
    const key = kv[1].toLowerCase(), val = kv[2].trim();
    if (section === "interface") {
      if (key === "privatekey") out.privKey = val;
      else if (key === "listenport") out.listenPort = val;
      else if (key === "address") out.addresses = val;
      else if (key === "dns") out.dns = val;
    } else if (section === "peer" && peer) {
      if (key === "publickey") peer.publicKey = val;
      else if (key === "presharedkey") peer.presharedKey = val;
      else if (key === "allowedips") peer.allowedIPs = val;
      else if (key === "persistentkeepalive") peer.keepalive = val;
      else if (key === "endpoint") { const e = splitEndpoint(val); peer.endpointHost = e.host; peer.endpointPort = e.port; }
    }
  }
  return out;
};
type Interface = { Name: string; Device?: string; Proto?: Proto };
type Device = { Name: string; Type?: string; Ports?: string[]; Ifname?: string; VID?: number };
// WGPeer mirrors the model's network/wireguard_peer kind (one remote endpoint of
// a WireGuard tunnel; Interface refs the local wg interface by Name).
type WGPeer = {
  Name: string; Interface: string; PublicKey?: string; PresharedKey?: string;
  AllowedIPs?: string[]; EndpointHost?: string; EndpointPort?: number;
  PersistentKeepalive?: number; RouteAllowedIPs?: boolean;
};
type Config = { Interfaces?: Interface[]; Devices?: Device[]; WGPeers?: WGPeer[]; Radios?: any[]; SSIDs?: any[]; [k: string]: any };
// WifiConn mirrors netd's wifi_status row (an active 802-11-wireless connection).
type WifiConn = { name: string; device: string };
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

// Icon pulls lucide glyphs from the shared shell sprite (/icons.svg) by symbol
// id — the same source the session sidebar uses — instead of inlining per-app
// copies. The networking glyphs are registered in web/shell/build-icons.mjs.
function Icon(p: { name: string; size?: number }) {
  return (
    <svg class="wash-net-ico" width={p.size ?? 14} height={p.size ?? 14}
      fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <use href={washAssetUrl(`icons.svg#${p.name}`)} />
    </svg>
  );
}

function NetApp(props: WashAppProps) {
  const [config, setConfig] = createSignal<Config>({ Interfaces: [], Devices: [] }); // committed baseline (from netd)
  const [draft, setDraft] = createSignal<Config>({ Interfaces: [], Devices: [] });  // staged edits, not yet applied
  const [caps, setCaps] = createSignal<Caps>(emptyCaps());
  const can = (f: string) => caps().features.has(f);
  const canKind = (k: string) => caps().kinds.has(k);
  const [links, setLinks] = createSignal<string[]>([]); // physical NICs from the backend
  const [adding, setAdding] = createSignal<null | "ethernet" | "vlan" | "bridge" | "wifi" | "wireguard">(null);

  // Wifi gating + state. wifiCapable shows the +Wifi button (the renderer can
  // express wifi AND a radio is present); wifiLive enables the live scan/connect
  // path (nmcli), else the dialog is manual-only and connects via Apply.
  const [wifiRadio, setWifiRadio] = createSignal(false);
  const [wifiLive, setWifiLive] = createSignal(false);
  const [wifiDevices, setWifiDevices] = createSignal<string[]>([]);
  const [wifiConns, setWifiConns] = createSignal<WifiConn[]>([]);
  const [aps, setAps] = createSignal<AP[]>([]);
  const [scanning, setScanning] = createSignal(false);
  const [wifiEnabled, setWifiEnabled] = createSignal(true); // NM's software wifi switch
  const wifiCapable = createMemo(() => canKind("wireless/wifi-iface") && wifiRadio());
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
    // wifi_radio/wifi_live are omitempty, so only `true` arrives here; the
    // authoritative (always-sent) values come from `current` in loadCurrent.
    if (s.wifi_radio === true) setWifiRadio(true);
    if (s.wifi_live === true) setWifiLive(true);
  };

  const loadCurrent = async () => {
    const r = await sendWithReply("current");
    if (r.kind === "current_ok") {
      const cfg = (r.config ?? { Interfaces: [], Devices: [] }) as Config;
      setConfig(cfg);
      setDraft(structuredClone(cfg)); // reset the draft to the freshly committed state
      setCaps(toCaps(r.caps));
      setLinks((r.devices ?? []) as string[]);
      setWifiRadio(!!r.wifi_radio);
      setWifiLive(!!r.wifi_live);
      setWifiDevices((r.wifi_devices ?? []) as string[]);
      void refreshWifi();
    }
  };

  // refreshWifi pulls the active wifi connections (NM-live only). Called on load
  // and after any connect/forget (and on a net.state Refresh, via loadCurrent).
  const refreshWifi = async () => {
    if (!wifiLive()) { setWifiConns([]); return; }
    const r = await sendWithReply("wifi_status");
    if (r.kind === "wifi_status_ok") setWifiConns((r.conns ?? []) as WifiConn[]);
  };

  // scanWifi pulls the live AP list (NM rescans + lists; rate-limit handled BE).
  const scanWifi = async () => {
    if (!wifiLive()) return;
    setScanning(true);
    const r = await sendWithReply("wifi_scan", {}, 15000);
    setScanning(false);
    if (r.kind === "wifi_scan_ok") {
      setAps((r.aps ?? []) as AP[]);
      if (typeof r.enabled === "boolean") setWifiEnabled(r.enabled);
    }
  };

  // toggleRadio flips NM's software wifi switch (privileged → escalates). The
  // scan poll picks up the new enabled state; nudge it once the action lands.
  const toggleRadio = (on: boolean) => {
    void sendWithReply("wifi_radio", { on }).then((r) => {
      if (r.kind !== "wifi_radio_ok") setStatus(`error: ${r.msg ?? r.code}`);
      window.setTimeout(() => void scanWifi(), 1500);
    });
  };

  // connectWifi routes by capability: NM-live → the imperative nmcli path
  // (wifi_connect; async, the result lands via a net.state Refresh). No NM →
  // declarative: fold the SSID into the config and apply (commit-confirm).
  const connectWifi = (ssid: string, security: string, psk: string, hidden: boolean) => {
    setAdding(null);
    if (wifiLive()) {
      void sendWithReply("wifi_connect", { ssid, security, psk, hidden }).then((r) => {
        if (r.kind !== "wifi_connect_ok") setStatus(`error: ${r.msg ?? r.code}`);
      });
      return;
    }
    connectWifiDeclarative(ssid, security, psk, hidden);
  };

  const connectWifiDeclarative = (ssid: string, security: string, psk: string, hidden: boolean) => {
    const dev = wifiDevices()[0] ?? "";
    if (!dev) { setStatus("error: no wifi device found"); return; }
    // Encryption union matches codec FE-JSON: {_tag:"none"} | {_tag,Key}.
    const enc = security === "none" ? { _tag: "none" } : { _tag: security, Key: psk };
    const cfg = structuredClone(config()) as Config;
    cfg.Radios = [...(cfg.Radios ?? []).filter((r: any) => r.Name !== dev), { Name: dev }];
    cfg.SSIDs = [
      ...(cfg.SSIDs ?? []).filter((s: any) => s.Device !== dev),
      { Device: dev, SSID: ssid, Mode: "sta", Network: dev, Hidden: hidden, Encryption: enc },
    ];
    setBusy(true); setStatus("applying"); setEvents([]);
    void sendWithReply("apply", { config: cfg }, 30000).then((r) => {
      setBusy(false);
      if (r.kind === "apply_ok") applyState({ status: r.state, events: r.events, confirm_window_ms: r.confirm_window_ms });
      else setStatus(`error: ${r.msg ?? r.code}`);
    });
  };

  const forgetWifi = (ssid: string) => {
    void sendWithReply("wifi_forget", { ssid }).then((r) => {
      if (r.kind !== "wifi_forget_ok") setStatus(`error: ${r.msg ?? r.code}`);
    });
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
  // addWireGuard stages a WireGuard tunnel: the local interface (proto carries
  // the private key / listen port / tunnel addresses) plus its peers, which live
  // in the separate WGPeers kind and ref the interface by Name. The wg device is
  // named after the interface itself (no physical NIC).
  const addWireGuard = (name: string, proto: Proto, peers: WGPeer[]) => {
    setDraft((d) => {
      const next = structuredClone(d);
      next.Interfaces = [...(next.Interfaces ?? []), { Name: name, Device: name, Proto: proto }];
      next.WGPeers = [...(next.WGPeers ?? []), ...peers.map((p) => ({ ...p, Interface: name }))];
      return next;
    });
    setAdding(null);
  };

  // Poll the scan while the dialog is open on an NM-live box (~2.5s; the effect
  // re-runs when `adding` changes and onCleanup clears the interval on close).
  createEffect(() => {
    if (adding() !== "wifi" || !wifiLive()) return;
    void scanWifi();
    const t = window.setInterval(() => void scanWifi(), 2500);
    onCleanup(() => window.clearInterval(t));
  });

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail;
      if (!m) return;
      if (m.kind === "net.state") {
        applyState(m.state);
        // netd read the box out-of-band (privileged escalation) — re-fetch so
        // the freshly-readable config replaces the "unconfigured" placeholder.
        if (m.state?.refresh) void loadCurrent();
        return;
      }
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
          <button data-testid="add-ethernet" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy()} onClick={() => { setConfigureDevice(""); setAdding("ethernet"); }}><Icon name="ethernet-port" /> Ethernet</button>
          <button data-testid="add-vlan" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy() || !can("vlan")} onClick={() => setAdding("vlan")}><Icon name="git-branch" /> VLAN</button>
          <button data-testid="add-bridge" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy() || !can("bridge")} onClick={() => setAdding("bridge")}><Icon name="git-merge" /> Bridge</button>
          <Show when={can("wireguard")}>
            <button data-testid="add-wireguard" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy()} onClick={() => setAdding("wireguard")}><Icon name="shield" /> WireGuard</button>
          </Show>
          <Show when={wifiCapable()}>
            <button data-testid="add-wifi" class="wash-net-btn" disabled={adding() !== null || editIface() !== null || busy()} onClick={() => setAdding("wifi")}><Icon name="wifi" /> Wi-Fi</button>
          </Show>
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
        <Show when={adding() === "wireguard"}>
          <WireGuardWizard onCancel={() => setAdding(null)} onCreate={addWireGuard} />
        </Show>
        <Show when={adding() === "wifi"}>
          <WifiDialog live={wifiLive()} busy={busy()} enabled={wifiEnabled()} aps={aps()} scanning={scanning()} onScan={() => void scanWifi()} onToggleRadio={toggleRadio} onConnect={connectWifi} onCancel={() => setAdding(null)} />
        </Show>

        <div class="wash-net-list">
          <For each={wifiConns()}>
            {(w) => (
              <div class="wash-net-conn" data-testid={`wifi-${w.name}`} data-kind="WiFi" data-device={w.device} data-status="active">
                <div class="wash-net-conn-main">
                  <span class="wash-net-conn-name"><Icon name="wifi" /> {w.name}</span>
                  <span class="wash-net-conn-kind">Wi-Fi</span>
                  <span class="wash-net-conn-dev">{w.device}</span>
                </div>
                <div class="wash-net-conn-actions">
                  <button class="wash-net-btn ghost" title="Forget this network" disabled={busy()} onClick={() => forgetWifi(w.name)}><Icon name="trash" /> Forget</button>
                </div>
              </div>
            )}
          </For>
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
                  <button class="wash-net-btn ghost" disabled={busy()} onClick={() => undoRemove(iface)}><Icon name="undo-2" /> Undo</button>
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

// WireGuardWizard builds a WG tunnel: the local endpoint (name + private key +
// optional listen port + tunnel addresses) and a list of peers. The private key
// seeds generated; only the PRIVATE key is needed here (the backend derives our
// public key on apply). Each peer needs at least a public key; empty peer rows
// are dropped on create.
function WireGuardWizard(props: { onCancel: () => void; onCreate: (name: string, proto: Proto, peers: WGPeer[]) => void }) {
  const [name, setName] = createSignal("wg0");
  const [privKey, setPrivKey] = createSignal(genWGPrivateKey());
  const [listenPort, setListenPort] = createSignal("");
  const [addresses, setAddresses] = createSignal("");
  const [peers, setPeers] = createSignal<PeerForm[]>([blankPeer()]);
  const [importText, setImportText] = createSignal("");
  const [importErr, setImportErr] = createSignal("");
  let fileInput: HTMLInputElement | undefined;

  const pubKey = () => wgPublicKey(privKey());
  const csv = (s: string): string[] => s.split(",").map((x) => x.trim()).filter(Boolean);
  const peerFilled = (p: PeerForm) => !!(p.publicKey || p.endpointHost || p.allowedIPs);
  const setPeer = (i: number, patch: Partial<PeerForm>) => setPeers((ps) => ps.map((p, j) => (j === i ? { ...p, ...patch } : p)));
  // A peer that has any field filled must carry a public key (the one required field).
  const canCreate = () => !!name() && !!privKey() && peers().every((p) => !peerFilled(p) || p.publicKey.trim() !== "");

  // applyImport fills the whole form from a pasted/loaded wg-quick config — the
  // exact text a WireGuard QR code encodes — so "scan on phone, paste here" works.
  const applyImport = (text: string) => {
    const c = parseWgConf(text);
    if (!c.privKey && !c.peers.length) { setImportErr("No [Interface]/[Peer] found — is this a WireGuard config?"); return; }
    setImportErr("");
    if (c.privKey) setPrivKey(c.privKey);
    if (c.listenPort) setListenPort(c.listenPort);
    if (c.addresses) setAddresses(c.addresses);
    setPeers(c.peers.length ? c.peers : [blankPeer()]);
  };

  const create = () => {
    const proto: Proto = { _tag: "wireguard", PrivateKey: privKey() };
    if (listenPort().trim()) proto.ListenPort = parseInt(listenPort(), 10);
    const addrs = csv(addresses());
    if (addrs.length) proto.Addresses = addrs;
    const built: WGPeer[] = peers().filter(peerFilled).map((p, i) => {
      const peer: WGPeer = { Name: `${name()}_peer${i}`, Interface: name(), PublicKey: p.publicKey.trim() };
      if (p.presharedKey.trim()) peer.PresharedKey = p.presharedKey.trim();
      if (p.endpointHost.trim()) peer.EndpointHost = p.endpointHost.trim();
      if (p.endpointPort.trim()) peer.EndpointPort = parseInt(p.endpointPort, 10);
      const aips = csv(p.allowedIPs);
      if (aips.length) peer.AllowedIPs = aips;
      if (p.keepalive.trim()) peer.PersistentKeepalive = parseInt(p.keepalive, 10);
      return peer;
    });
    props.onCreate(name(), proto, built);
  };

  return (
    <div class="wash-net-wizard" data-testid="wireguard-wizard">
      <div class="wash-net-wizard-title">New WireGuard tunnel</div>

      <div class="wash-net-wg-import">
        <textarea data-testid="wg-import-text" class="wash-net-wg-importbox" rows="3"
          placeholder="Paste a WireGuard config (a QR code's contents), or load a .conf file — it fills in everything below."
          value={importText()} onInput={(e) => setImportText(e.currentTarget.value)} />
        <div class="wash-net-wg-importbar">
          <button type="button" class="wash-net-btn ghost" data-testid="wg-import-file" onClick={() => fileInput?.click()}>Load .conf…</button>
          <input ref={fileInput} type="file" accept=".conf,text/plain" style={{ display: "none" }}
            onChange={(e) => { const f = e.currentTarget.files?.[0]; if (f) void f.text().then(applyImport); e.currentTarget.value = ""; }} />
          <button type="button" class="wash-net-btn" data-testid="wg-import-apply" disabled={!importText().trim()} onClick={() => applyImport(importText())}>Import</button>
          <Show when={importErr()}><span class="wash-net-wg-importerr" data-testid="wg-import-err">{importErr()}</span></Show>
        </div>
      </div>

      <label class="wash-net-field">
        <span class="wash-net-label">Name</span>
        <input data-testid="wg-name" value={name()} onInput={(e) => setName(e.currentTarget.value)} />
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">Private key</span>
        <span class="wash-net-wg-key">
          <input data-testid="wg-privkey" type="password" value={privKey()} onInput={(e) => setPrivKey(e.currentTarget.value)} />
          <button type="button" class="wash-net-btn ghost" data-testid="wg-genkey" onClick={() => setPrivKey(genWGPrivateKey())}>Generate</button>
        </span>
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">Public key</span>
        <span class="wash-net-wg-key">
          <input data-testid="wg-pubkey" readOnly value={pubKey()} placeholder="(derived from the private key)" />
          <button type="button" class="wash-net-btn ghost" data-testid="wg-copy-pubkey" disabled={!pubKey()} onClick={() => void navigator.clipboard?.writeText(pubKey())}>Copy</button>
        </span>
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">Listen port</span>
        <input data-testid="wg-listenport" type="number" min="0" max="65535" value={listenPort()} placeholder="auto" onInput={(e) => setListenPort(e.currentTarget.value)} />
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">Addresses</span>
        <input data-testid="wg-addresses" value={addresses()} placeholder="10.9.0.1/24, fd00::1/64" onInput={(e) => setAddresses(e.currentTarget.value)} />
      </label>

      <div class="wash-net-grouplabel">Peers</div>
      <For each={peers()}>
        {(p, i) => (
          <div class="wash-net-wg-peer" data-testid={`wg-peer-${i()}`}>
            <label class="wash-net-field"><span class="wash-net-label">Public key</span>
              <input data-testid={`wg-peer-pubkey-${i()}`} value={p.publicKey} onInput={(e) => setPeer(i(), { publicKey: e.currentTarget.value })} /></label>
            <label class="wash-net-field"><span class="wash-net-label">Preshared key</span>
              <input data-testid={`wg-peer-psk-${i()}`} type="password" placeholder="optional" value={p.presharedKey} onInput={(e) => setPeer(i(), { presharedKey: e.currentTarget.value })} /></label>
            <label class="wash-net-field"><span class="wash-net-label">Endpoint</span>
              <span class="wash-net-wg-endpoint">
                <input data-testid={`wg-peer-host-${i()}`} placeholder="host" value={p.endpointHost} onInput={(e) => setPeer(i(), { endpointHost: e.currentTarget.value })} />
                <input data-testid={`wg-peer-port-${i()}`} type="number" placeholder="port" value={p.endpointPort} onInput={(e) => setPeer(i(), { endpointPort: e.currentTarget.value })} />
              </span></label>
            <label class="wash-net-field"><span class="wash-net-label">Allowed IPs</span>
              <input data-testid={`wg-peer-allowed-${i()}`} placeholder="0.0.0.0/0, ::/0" value={p.allowedIPs} onInput={(e) => setPeer(i(), { allowedIPs: e.currentTarget.value })} /></label>
            <label class="wash-net-field"><span class="wash-net-label">Keepalive (s)</span>
              <input data-testid={`wg-peer-keepalive-${i()}`} type="number" placeholder="off" value={p.keepalive} onInput={(e) => setPeer(i(), { keepalive: e.currentTarget.value })} /></label>
            <Show when={peers().length > 1}>
              <button type="button" class="wash-net-btn ghost" data-testid={`wg-peer-remove-${i()}`} onClick={() => setPeers((ps) => ps.filter((_, j) => j !== i()))}><Icon name="trash" /> Remove peer</button>
            </Show>
          </div>
        )}
      </For>
      <button type="button" class="wash-net-btn ghost" data-testid="wg-add-peer" onClick={() => setPeers((ps) => [...ps, blankPeer()])}><Icon name="plus" /> Add peer</button>

      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button data-testid="wg-create" class="wash-net-btn primary" disabled={!canCreate()} onClick={create}>Create</button>
      </div>
    </div>
  );
}

// Chrome (surfaces, borders, text, fonts, radii) and accent text colors
// pull from @wash/ui tokens. The remaining bare hexes below are a
// net-local palette with no shared-token equivalent: the dark outline-
// chip border companions to the accents (#2e5a38/#4a4030/#5a2e2e/
// #33415a), the brand "primary" button blue (#3a5a9a/#456bb5), the
// apply-terminal confirm-banner ambers, and the WireGuard import box.
const STYLE = `
.wash-net-app { display:flex; flex-direction:column; height:100%; background:${tokens.bgWindow}; color:${tokens.fg};
  font:${tokens.fontSizeBase} ${tokens.fontSans}; position:relative; }
.wash-net-head { display:flex; align-items:center; justify-content:space-between; padding:8px 12px; border-bottom:1px solid ${tokens.borderMenu}; }
.wash-net-head h1 { font-size:14px; font-weight:600; margin:0; }
.wash-net-add { display:flex; gap:6px; }
.wash-net-body { flex:1; overflow:auto; padding:10px 12px; }
.wash-net-list { display:flex; flex-direction:column; gap:6px; }
.wash-net-empty { opacity:.6; font-size:12px; padding:16px; text-align:center; }
.wash-net-conn { display:grid; grid-template-columns:1fr auto; grid-template-rows:auto auto; gap:2px 8px;
                 border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusLg}px; padding:8px 10px; align-items:center; }
.wash-net-conn-main { display:flex; align-items:baseline; gap:8px; }
.wash-net-conn-name { font-weight:600; font-size:13px; }
.wash-net-conn-kind { font-size:10px; text-transform:uppercase; letter-spacing:.04em; color:${tokens.accentBlue}; border:1px solid #33415a; border-radius:9px; padding:1px 7px; }
.wash-net-conn-dev { font-size:11px; opacity:.6; font-family:${tokens.fontMono}; }
.wash-net-conn-sub { grid-column:1; font-size:11px; opacity:.7; }
.wash-net-conn-actions { grid-row:1 / span 2; grid-column:2; display:flex; gap:4px; align-items:center; }
.wash-net-conn[data-status="new"] { border-color:#2e5a38; }
.wash-net-conn[data-status="edited"] { border-color:#4a4030; }
.wash-net-conn.removed { opacity:.6; border-style:dashed; }
.wash-net-conn.removed .wash-net-conn-name { text-decoration:line-through; }
.wash-net-badge { font-size:9px; text-transform:uppercase; letter-spacing:.05em; border-radius:${tokens.radiusXl}px; padding:1px 6px; border:1px solid; }
.wash-net-badge[data-badge="new"] { color:${tokens.accentGreen}; border-color:#2e5a38; }
.wash-net-badge[data-badge="edited"] { color:${tokens.accentAmber}; border-color:#4a4030; }
.wash-net-badge[data-badge="removed"] { color:${tokens.accentRed}; border-color:#5a2e2e; }
.wash-net-pending { display:flex; align-items:center; gap:10px; padding:8px 12px; border-top:1px solid ${tokens.borderMenu};
  background:#1a1a30; flex-shrink:0; }
.wash-net-pending-msg { flex:1; font-size:12px; color:${tokens.accentAmber}; }
.wash-net-greyed { display:flex; gap:10px; align-items:center; margin-top:14px; padding-top:10px; border-top:1px dashed ${tokens.borderMenu}; }
.wash-net-lock { font-size:12px; opacity:.45; }
.wash-net-hint { font-size:11px; opacity:.4; font-style:italic; }
.wash-net-wizard { border:1px solid ${tokens.borderFocus}; border-radius:${tokens.radiusXl}px; padding:10px 12px; margin-bottom:12px; background:${tokens.bgMenu}; }
.wash-net-wizard-title { font-size:13px; font-weight:600; margin-bottom:8px; }
.wash-net-wizard-actions { display:flex; justify-content:flex-end; gap:8px; margin-top:10px; }
.wash-net-field { display:grid; grid-template-columns:130px 1fr; align-items:center; gap:8px; margin:5px 0; }
.wash-net-label { font-size:12px; opacity:.85; }
.wash-net-derived { font-size:12px; font-family:${tokens.fontMono}; opacity:.8; }
.wash-net-field input, .wash-net-field select, .wash-net-field textarea {
  background:${tokens.bgMenu}; color:${tokens.fg}; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; padding:3px 6px; font:inherit; }
.wash-net-field input:focus, .wash-net-field select:focus, .wash-net-field textarea:focus {
  outline:none; border-color:${tokens.borderFocus}; }
.wash-net-field textarea { resize:vertical; font:${tokens.fontSizeMd} ${tokens.fontMono}; }
.wash-net-reflist { display:flex; flex-wrap:wrap; gap:8px; }
.wash-net-reflist label { display:flex; gap:4px; align-items:center; font-size:12px; }
.wash-net-diag { grid-column:2; font-size:11px; color:${tokens.accentAmber}; }
.wash-net-field.error input, .wash-net-field.error select, .wash-net-field.error textarea { border-color:${tokens.borderDanger}; }
.wash-net-field.error .wash-net-diag { color:${tokens.accentRed}; }
.wash-net-members { display:flex; flex-wrap:wrap; gap:10px; }
.wash-net-addressing { margin-top:6px; }
.wash-net-addressing .wash-net-form { padding:0; }
.wash-net-group { border:none; padding:0; margin:0; }
.wash-net-method { margin:5px 0; }
.wash-net-method select { width:100%; background:${tokens.bgMenu}; color:${tokens.fg}; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; padding:4px 6px; font:inherit; }
.wash-net-method select:focus { outline:none; border-color:${tokens.borderFocus}; }
.wash-net-grouplabel { font-size:11px; opacity:.6; text-transform:uppercase; letter-spacing:.04em; margin:6px 0 2px; }
.wash-net-wg-key { display:flex; gap:6px; }
.wash-net-wg-key input { flex:1; min-width:0; }
.wash-net-wg-endpoint { display:flex; gap:6px; }
.wash-net-wg-endpoint input:first-child { flex:1; min-width:0; }
.wash-net-wg-endpoint input:last-child { width:72px; }
.wash-net-wg-peer { border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; padding:6px 8px; margin:4px 0; }
.wash-net-wg-import { display:flex; flex-direction:column; gap:6px; margin:4px 0 8px; padding:8px;
  background:#15151f; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; }
.wash-net-wg-importbox { width:100%; box-sizing:border-box; resize:vertical; font:${tokens.fontSizeSm} ${tokens.fontMono};
  background:#101018; color:#cfd0d4; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusSm}px; padding:5px 6px; }
.wash-net-wg-importbar { display:flex; align-items:center; gap:6px; }
.wash-net-wg-importerr { color:#e0a0a0; font-size:11px; }
.wash-net-addressing .wash-net-group { margin:0 0 6px; }
.wash-net-member { font-size:12px; display:flex; gap:4px; align-items:center; }
.wash-net-wifi-scan { margin:4px 0 10px; }
.wash-net-wifi-scanhead { display:flex; align-items:center; justify-content:space-between; }
.wash-net-wifi-off { display:flex; align-items:center; gap:10px; padding:8px 0; }
.wash-net-aplist { display:flex; flex-direction:column; gap:3px; max-height:160px; overflow:auto; margin-top:4px; }
.wash-net-ap { display:flex; align-items:center; justify-content:space-between; gap:8px; width:100%;
  background:${tokens.bgMenu}; color:${tokens.fg}; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; padding:5px 9px; font:inherit; cursor:pointer; text-align:left; }
.wash-net-ap:hover { background:#22223a; border-color:${tokens.borderFocus}; }
.wash-net-ap[data-inuse="1"] { border-color:#2e5a38; }
.wash-net-ap-ssid { font-weight:500; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.wash-net-ap-meta { font-size:12px; opacity:.7; flex-shrink:0; font-family:${tokens.fontMono}; }
.wash-net-btn { display:inline-flex; align-items:center; gap:5px; background:${tokens.bgRowHover}; color:${tokens.fg}; border:1px solid ${tokens.borderMenu}; border-radius:${tokens.radiusMd}px; padding:4px 10px; font:inherit; cursor:pointer; }
.wash-net-ico { flex-shrink:0; opacity:.9; }
.wash-net-btn:hover:not(:disabled) { background:${tokens.bgRowSelected}; }
.wash-net-btn.primary { background:#3a5a9a; border-color:#3a5a9a; color:#fff; }
.wash-net-btn.primary:hover:not(:disabled) { background:#456bb5; }
.wash-net-btn.ghost { background:transparent; opacity:.7; }
.wash-net-btn:disabled { opacity:.4; cursor:default; }

/* --- apply terminal (B3) --- */
.wash-net-apply { border-top:1px solid ${tokens.borderMenu}; display:flex; flex-direction:column; flex-shrink:0; }
.wash-net-rail { display:flex; align-items:center; gap:6px; padding:8px 12px 4px; }
.wash-net-chip { font-size:10px; text-transform:uppercase; letter-spacing:.04em; padding:2px 8px; border-radius:10px; border:1px solid #33363d; color:#7a7d85; }
.wash-net-chip[data-state="done"] { color:${tokens.accentGreen}; border-color:#2e5a38; }
.wash-net-chip[data-state="active"] { color:${tokens.accentAmber}; border-color:#4a4030; animation:wash-pulse 1.2s ease-in-out infinite; }
.wash-net-chip[data-state="bad"] { color:${tokens.accentRed}; border-color:#5a2e2e; }
@keyframes wash-pulse { 0%,100% { opacity:1; } 50% { opacity:.5; } }
.wash-net-log { max-height:120px; overflow:auto; margin:0 12px; padding:4px 0; font:${tokens.fontSizeSm} ${tokens.fontMono}; }
.wash-net-logline { display:flex; gap:8px; padding:1px 0; }
.wash-net-logphase { color:#6a6d75; min-width:80px; text-transform:uppercase; }
.wash-net-logline[data-level="warn"] .wash-net-logmsg { color:${tokens.accentAmber}; }
.wash-net-logline[data-level="error"] .wash-net-logmsg { color:${tokens.accentRed}; }
.wash-net-applybar { display:flex; align-items:center; gap:8px; padding:8px 12px; }
.wash-net-status { flex:1; font-size:12px; opacity:.8; font-variant:tabular-nums; }
/* Confirm prompt: a banner pinned to the top of the app (over the header),
 * so Keep/Discard are reachable even when the window's bottom is clipped
 * under the taskbar on a short screen. See ApplyTerminal.tsx. */
.wash-net-confirm { position:absolute; top:0; left:0; right:0; z-index:50;
  display:flex; align-items:center; gap:8px; padding:8px 12px;
  background:#23201a; border-bottom:1px solid #4a4030; box-shadow:0 4px 12px rgba(0,0,0,.45); }
.wash-net-countdown { flex:1; position:relative; height:22px; border-radius:${tokens.radiusMd}px; overflow:hidden; background:#1b1d22; border:1px solid #4a4030; display:flex; align-items:center; }
.wash-net-countbar { position:absolute; inset:0 auto 0 0; background:rgba(208,160,64,.18); transition:width .25s linear; }
.wash-net-counttext { position:relative; padding:0 8px; font-size:11px; color:#e8c878; }
`;

defineWashApp("wash-app-net", NetApp, { style: `display:block;height:100%;overflow:hidden;font:${tokens.fontSizeBase}/1.5 ${tokens.fontSans};color:${tokens.fg};` });

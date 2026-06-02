// WifiDialog — the advanced/manual Wi-Fi form (Tier 1), shown whenever the
// backend can express wifi and a radio is present. On an NM-live box it also
// hosts the scan/connect picker (Tier 2, added separately); without nmcli it's
// manual-only and connects declaratively through Apply. SSID + security + PSK
// (+ hidden) is all `nmcli device wifi connect` / a netplan wifis: stanza needs.

import { createSignal, For, Show } from "solid-js";

export type AP = { ssid: string; signal: number; security: string; in_use: boolean };

const SECURITIES: { tag: string; label: string }[] = [
  { tag: "none", label: "Open (no password)" },
  { tag: "psk2", label: "WPA2 (PSK)" },
  { tag: "sae", label: "WPA3 (SAE)" },
];

// secTagFromAP maps nmcli's SECURITY column to our union tag — WPA3 → sae,
// any other WPA → psk2, blank → open.
export function secTagFromAP(security: string): string {
  if (/WPA3|SAE/i.test(security)) return "sae";
  if (/WPA|RSN|WEP/i.test(security)) return "psk2";
  return "none";
}

// signalBars renders a 0–100 RSSI as 1–4 block glyphs (BMP chars, one UTF-16
// unit each, so slice is safe).
function signalBars(s: number): string {
  const n = s >= 75 ? 4 : s >= 50 ? 3 : s >= 25 ? 2 : 1;
  return "▂▄▆█".slice(0, n);
}

export function WifiDialog(props: {
  live: boolean;
  busy: boolean;
  aps?: AP[];
  scanning?: boolean;
  onScan?: () => void;
  onConnect: (ssid: string, security: string, psk: string, hidden: boolean) => void;
  onCancel: () => void;
}) {
  const [ssid, setSsid] = createSignal("");
  const [security, setSecurity] = createSignal("psk2");
  const [psk, setPsk] = createSignal("");
  const [hidden, setHidden] = createSignal(false);

  let pskInput: HTMLInputElement | undefined;

  const needsPsk = () => security() !== "none";
  // WPA2/WPA3 PSKs are 8–63 chars; enforce before enabling Connect.
  const pskOk = () => !needsPsk() || (psk().length >= 8 && psk().length <= 63);
  const canConnect = () => !!ssid() && pskOk() && !props.busy;

  // Clicking a scanned AP prefills the manual form (the two flows are one form,
  // one populated by a click, one by typing) and jumps to the password.
  const pickAP = (ap: AP) => {
    setSsid(ap.ssid);
    const sec = secTagFromAP(ap.security);
    setSecurity(sec);
    if (sec !== "none") pskInput?.focus();
  };

  return (
    <div class="wash-net-wizard" data-testid="wifi-dialog">
      <div class="wash-net-wizard-title">{props.live ? "Add Wi-Fi network" : "Add Wi-Fi network (manual)"}</div>

      <Show when={props.live}>
        <div class="wash-net-wifi-scan">
          <div class="wash-net-wifi-scanhead">
            <span class="wash-net-grouplabel">Available networks</span>
            <button data-testid="wifi-scan" class="wash-net-btn ghost" disabled={!!props.scanning} onClick={() => props.onScan?.()}>
              {props.scanning ? "Scanning…" : "Scan"}
            </button>
          </div>
          <div class="wash-net-aplist">
            <For each={props.aps ?? []} fallback={<div class="wash-net-hint">{props.scanning ? "Scanning…" : "No networks found yet."}</div>}>
              {(ap) => (
                <button type="button" class="wash-net-ap" data-testid={`ap-${ap.ssid}`} data-inuse={ap.in_use ? "1" : "0"} onClick={() => pickAP(ap)}>
                  <span class="wash-net-ap-ssid">{ap.ssid || "(hidden)"}</span>
                  <span class="wash-net-ap-meta">
                    <Show when={ap.security}>🔒 </Show>{signalBars(ap.signal)}<Show when={ap.in_use}> ✓</Show>
                  </span>
                </button>
              )}
            </For>
          </div>
        </div>
      </Show>

      <label class="wash-net-field">
        <span class="wash-net-label">Network (SSID)</span>
        <input data-testid="wifi-ssid" value={ssid()} onInput={(e) => setSsid(e.currentTarget.value)} />
      </label>
      <label class="wash-net-field">
        <span class="wash-net-label">Security</span>
        <select data-testid="wifi-security" value={security()} onChange={(e) => setSecurity(e.currentTarget.value)}>
          <For each={SECURITIES}>{(s) => <option value={s.tag}>{s.label}</option>}</For>
        </select>
      </label>
      <Show when={needsPsk()}>
        <label class="wash-net-field">
          <span class="wash-net-label">Password</span>
          <input data-testid="wifi-psk" ref={pskInput} type="password" value={psk()} placeholder="8–63 characters"
            onInput={(e) => setPsk(e.currentTarget.value)} />
        </label>
      </Show>
      <label class="wash-net-field">
        <span class="wash-net-label">Hidden network</span>
        <input data-testid="wifi-hidden" type="checkbox" checked={hidden()} onChange={(e) => setHidden(e.currentTarget.checked)} />
      </label>

      <Show when={!props.live}>
        <div class="wash-net-hint">No live scanning on this backend — entering the network manually (applied through Apply, with the usual confirm window).</div>
      </Show>

      <div class="wash-net-wizard-actions">
        <button class="wash-net-btn" onClick={props.onCancel}>Cancel</button>
        <button data-testid="wifi-connect" class="wash-net-btn primary" disabled={!canConnect()}
          onClick={() => props.onConnect(ssid(), security(), psk(), hidden())}>Connect</button>
      </div>
    </div>
  );
}

// defineSettingsPanel — the settings-panel analogue of defineWashApp.
//
// A panel bundle (panel.js) an app ships calls this to register its
// `wash-settings-panel-<x>` custom element. Unlike a normal app, a
// panel element is NOT its own router instance — the settings app mounts
// it INSIDE its own element. So the panel can't use window.wash directly;
// instead the settings host builds a SettingsPanelPort, assigns it to the
// element as the `washPanelPort` property BEFORE the element connects,
// and the panel reads it in connectedCallback.
//
// All transport goes through the settings BE's generic svc.* / settings.*
// / launch verbs, so the owning service needs no awareness of being
// hosted (its messages still arrive router-attested from com.wash.settings).

import { render } from 'solid-js/web';
import type { Component } from 'solid-js';

/** SettingsPanelPort is the host-provided bridge a panel uses to talk to
 *  its owning service and the settings host. */
export interface SettingsPanelPort {
  /** The panel's owning app id — the default target for send/restart/onMessage. */
  appID: string;
  /** Relay an app_msg to the service via the settings BE (svc.send), so the
   *  service sees a router-attested sender. Pass `app` to target another app. */
  send(payload: Record<string, unknown>, app?: string): void;
  /** Cycle a background singleton via the router (svc.restart). Defaults to appID. */
  restart(app?: string): void;
  /** Subscribe to svc.recv payloads pushed by `app` (default appID). Returns an
   *  unsubscribe. */
  onMessage(cb: (payload: Record<string, unknown>) => void, app?: string): () => void;
  /** Ask the settings store for a config domain's value (settings.read); cb
   *  fires with each settings.value for that domain. Returns an unsubscribe. */
  readConfig(domain: string, cb: (value: Record<string, unknown>) => void): () => void;
  /** Persist a config domain value (settings.write). */
  writeConfig(domain: string, value: Record<string, unknown>): void;
  /** Spawn a windowed app (the host's launch verb). */
  launch(app: string): void;
}

/** Props every settings-panel component receives. */
export interface SettingsPanelProps {
  /** The host custom element. */
  host: HTMLElement;
  /** The bridge to the service + settings host. */
  port: SettingsPanelPort;
}

/** Element property the settings host sets the port on before append. */
export const PANEL_PORT_PROP = 'washPanelPort';

export interface DefineSettingsPanelOptions {
  /** Inline style applied to the host element on connect. */
  style?: string;
}

/**
 * defineSettingsPanel registers `tag` as a custom element that mounts
 * `Panel` on connect (reading its port from the `washPanelPort` property)
 * and disposes it on disconnect. Re-registering a tag is a no-op.
 */
export function defineSettingsPanel(
  tag: `wash-settings-panel-${string}`,
  Panel: Component<SettingsPanelProps>,
  options: DefineSettingsPanelOptions = {},
): void {
  if (customElements.get(tag)) return;

  class WashSettingsPanelElement extends HTMLElement {
    [PANEL_PORT_PROP]?: SettingsPanelPort;
    private cleanup?: () => void;

    connectedCallback() {
      if (options.style) this.style.cssText = options.style;
      const port = this[PANEL_PORT_PROP];
      if (!port) {
        // Defensive: the host always assigns the port before append.
        // Without one there's nothing to render against.
        return;
      }
      this.cleanup = render(() => Panel({ host: this, port }), this);
    }

    disconnectedCallback() {
      this.cleanup?.();
      this.cleanup = undefined;
    }
  }

  customElements.define(tag, WashSettingsPanelElement);
}

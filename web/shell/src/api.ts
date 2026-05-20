// Shell-host API exposed to mounted app elements as `window.wash`.
// Apps inside custom elements use this to listen to chrome state
// (catalog, open windows) and to request actions (spawn via app_msg,
// focus, close).

export interface CatalogApp {
  id: string;
  name: string;
  icon?: string;
  surface: string;
  instancing: string;
  disabled?: boolean;
  reason?: string;
}

export interface WindowInfo {
  windowID: number;
  instanceID: string;
  element: string;
  title: string;
  focused: boolean;
  state: 'normal' | 'minimized' | 'maximized';
}

export type Listener<T> = (v: T) => void;

// Sub is a tiny pub/sub holding the current value plus a set of
// listeners. `on` fires the listener immediately with the current
// value so subscribers don't need a separate "get".
export class Sub<T> {
  private current: T;
  private listeners = new Set<Listener<T>>();

  constructor(initial: T) {
    this.current = initial;
  }

  get value(): T {
    return this.current;
  }

  set(v: T): void {
    this.current = v;
    for (const l of this.listeners) l(v);
  }

  on(cb: Listener<T>): () => void {
    this.listeners.add(cb);
    cb(this.current);
    return () => {
      this.listeners.delete(cb);
    };
  }
}

// mountedElements tracks the live custom element for each declared
// instance id. registerMountedElement is called from Desktop /
// FloatingWindow once an element is in the DOM; deliverToInstance
// queues messages that arrive before the element mounts so nothing is
// dropped during the race between addWindow and Solid's onMount.
const mountedElements = new Map<string, HTMLElement>();
const pendingMessages = new Map<string, unknown[]>();

export function registerMountedElement(instanceID: string, el: HTMLElement): void {
  mountedElements.set(instanceID, el);
  const q = pendingMessages.get(instanceID);
  if (q) {
    pendingMessages.delete(instanceID);
    for (const data of q) {
      el.dispatchEvent(new CustomEvent('wash:msg', { detail: data, bubbles: false }));
    }
  }
}

export function unregisterMountedElement(instanceID: string): void {
  mountedElements.delete(instanceID);
  pendingMessages.delete(instanceID);
}

export function deliverToInstance(instanceID: string, data: unknown): void {
  const el = mountedElements.get(instanceID);
  if (el) {
    el.dispatchEvent(new CustomEvent('wash:msg', { detail: data, bubbles: false }));
    return;
  }
  let q = pendingMessages.get(instanceID);
  if (!q) {
    q = [];
    pendingMessages.set(instanceID, q);
  }
  q.push(data);
}

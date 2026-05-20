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

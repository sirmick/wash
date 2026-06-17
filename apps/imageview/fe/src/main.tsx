// wash-app-imageview — a small single-image viewer. Left: a thumbnail list
// of the sibling images. Right: the selected image with wheel-zoom, drag-pan,
// fit/reset, and arrow-key prev/next. Image bytes and thumbnails stream from
// the BE over a raw channel via @wash/ui's createFileClient (no HTTP), so it
// works on the in-browser VM too.

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { ChevronLeft, ChevronRight, Image as ImageIcon, Maximize, ZoomIn, ZoomOut } from 'lucide-solid';
import { createFileClient, defineWashApp, tokens } from '@wash/ui';
import type { FileClient } from '@wash/ui';

interface ImageItem {
  name: string;
  path: string;
}

const THUMB_DIM = 96;
const MIN_ZOOM = 0.1;
const MAX_ZOOM = 8;
// Extensions internal/thumbs can decode → show a real thumbnail; others
// (webp/svg/…) still display full-res in the main view, but list as an icon.
const THUMB_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif']);
const isThumbable = (name: string): boolean => {
  const i = name.lastIndexOf('.');
  return i > 0 && THUMB_EXTS.has(name.slice(i + 1).toLowerCase());
};

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [images, setImages] = createSignal<ImageItem[]>([]);
  const [index, setIndex] = createSignal(0);
  const [zoom, setZoom] = createSignal(1);
  const [pan, setPan] = createSignal({ x: 0, y: 0 });
  const [mainUrl, setMainUrl] = createSignal<string | null>(null);

  const fileClient: FileClient = createFileClient({ instance: props.instance, host: props.host });
  const send = (m: unknown) => window.wash.sendAppMsg(props.instance, m);
  const current = (): ImageItem | null => images()[index()] ?? null;

  const resetView = () => {
    setZoom(1);
    setPan({ x: 0, y: 0 });
  };
  const select = (i: number) => {
    if (i < 0 || i >= images().length) return;
    setIndex(i);
    resetView();
  };
  const step = (d: number) => {
    const n = images().length;
    if (n === 0) return;
    select((index() + d + n) % n);
  };
  const zoomBy = (factor: number) =>
    setZoom((z) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z * factor)));

  // Load the full-resolution bytes for the current image. Guarded so a
  // fast prev/next doesn't show a stale image when an earlier load lands late.
  createEffect(() => {
    const c = current();
    if (!c) {
      setMainUrl(null);
      return;
    }
    let alive = true;
    setMainUrl(null);
    fileClient
      .url(c.path)
      .then((u) => alive && setMainUrl(u))
      .catch(() => alive && setMainUrl(null));
    onCleanup(() => {
      alive = false;
    });
  });

  // ---- pan (drag) ----
  let dragging = false;
  let lastX = 0;
  let lastY = 0;
  const onDown = (e: MouseEvent) => {
    dragging = true;
    lastX = e.clientX;
    lastY = e.clientY;
  };
  const onMove = (e: MouseEvent) => {
    if (!dragging) return;
    const dx = e.clientX - lastX;
    const dy = e.clientY - lastY;
    lastX = e.clientX;
    lastY = e.clientY;
    setPan((p) => ({ x: p.x + dx, y: p.y + dy }));
  };
  const onUp = () => {
    dragging = false;
  };
  const onWheel = (e: WheelEvent) => {
    e.preventDefault();
    zoomBy(e.deltaY < 0 ? 1.1 : 1 / 1.1);
  };

  onMount(() => {
    const onMsg = (ev: Event) => {
      const m = (ev as CustomEvent).detail as
        | { kind?: string; dir?: string; images?: ImageItem[]; path?: string }
        | undefined;
      if (m?.kind === 'scan_ok') {
        const imgs = m.images ?? [];
        setImages(imgs);
        setIndex(0);
        resetView();
      } else if (m?.kind === 'open_file' && m.path) {
        // piece 4: the router launched us on a specific image. Scan its
        // folder, then select that file once the list lands.
        wantPath = m.path;
        send({ kind: 'scan', dir: dirOf(m.path) });
      }
    };
    // After scan_ok, jump to the requested open path if any.
    createEffect(() => {
      if (!wantPath) return;
      const i = images().findIndex((im) => im.path === wantPath);
      if (i >= 0) {
        setIndex(i);
        resetView();
        wantPath = '';
      }
    });

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
        e.preventDefault();
        step(1);
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
        e.preventDefault();
        step(-1);
      } else if (e.key === '+' || e.key === '=') {
        zoomBy(1.2);
      } else if (e.key === '-') {
        zoomBy(1 / 1.2);
      } else if (e.key === '0') {
        resetView();
      }
    };

    props.host.addEventListener('wash:msg', onMsg);
    props.host.addEventListener('keydown', onKey);
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    if (!props.host.hasAttribute('tabindex')) props.host.setAttribute('tabindex', '0');
    // Default scan (the open path, if any, arrives via wash:msg open_file).
    send({ kind: 'scan' });

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('keydown', onKey);
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
      fileClient.dispose();
    });
  });

  // wantPath: a path the router asked us to open, applied once it appears
  // in the scanned list. Plain var — only read/written on the same tick.
  let wantPath = '';

  return (
    <>
      <div data-testid="iv-list" style={listStyle}>
        <Show
          when={images().length > 0}
          fallback={<div style={{ padding: '16px', color: tokens.fgDim, 'font-size': '13px' }}>No images</div>}
        >
          <For each={images()}>
            {(img, i) => (
              <Thumb
                img={img}
                active={i() === index()}
                fileUrl={(p) => fileClient.url(p, { dim: THUMB_DIM })}
                onClick={() => select(i())}
              />
            )}
          </For>
        </Show>
      </div>

      <div data-testid="iv-main" style={mainStyle} onWheel={onWheel} onMouseDown={onDown}>
        <Show when={mainUrl()} fallback={<ImageIcon size={72} color={tokens.fgDim} />}>
          <img
            data-testid="iv-image"
            src={mainUrl()!}
            alt={current()?.name ?? ''}
            draggable={false}
            style={{
              transform: `translate(${pan().x}px, ${pan().y}px) scale(${zoom()})`,
              'max-width': '100%',
              'max-height': '100%',
              'object-fit': 'contain',
              cursor: 'grab',
              'user-select': 'none',
              'will-change': 'transform',
            }}
          />
        </Show>

        <div style={toolbarStyle}>
          <button type="button" data-testid="iv-prev" title="Previous (←)" style={tbBtnStyle} onClick={() => step(-1)}>
            <ChevronLeft size={16} />
          </button>
          <button type="button" data-testid="iv-zoom-out" title="Zoom out (-)" style={tbBtnStyle} onClick={() => zoomBy(1 / 1.2)}>
            <ZoomOut size={16} />
          </button>
          <button type="button" data-testid="iv-fit" title="Fit / reset (0)" style={tbBtnStyle} onClick={resetView}>
            <Maximize size={16} />
          </button>
          <button type="button" data-testid="iv-zoom-in" title="Zoom in (+)" style={tbBtnStyle} onClick={() => zoomBy(1.2)}>
            <ZoomIn size={16} />
          </button>
          <button type="button" data-testid="iv-next" title="Next (→)" style={tbBtnStyle} onClick={() => step(1)}>
            <ChevronRight size={16} />
          </button>
        </div>

        <Show when={current()}>
          <div data-testid="iv-caption" style={captionStyle}>
            {current()!.name} · {index() + 1}/{images().length} · {Math.round(zoom() * 100)}%
          </div>
        </Show>
      </div>
    </>
  );
};

// Thumb lazily fetches its thumbnail when scrolled into view, falling back
// to an image glyph while loading / for non-thumbnailable formats.
const Thumb: Component<{
  img: ImageItem;
  active: boolean;
  fileUrl: (path: string) => Promise<string>;
  onClick: () => void;
}> = (props) => {
  const [url, setUrl] = createSignal<string | null>(null);
  let el: HTMLDivElement | undefined;
  onMount(() => {
    if (!isThumbable(props.img.name) || !el) return;
    const io = new IntersectionObserver(
      (entries) => {
        for (const en of entries) {
          if (!en.isIntersecting) continue;
          io.disconnect();
          props.fileUrl(props.img.path).then(setUrl).catch(() => undefined);
        }
      },
      { rootMargin: '150px' },
    );
    io.observe(el);
    onCleanup(() => io.disconnect());
  });
  return (
    <div
      ref={el}
      data-testid={`iv-thumb-${props.img.name}`}
      style={{ ...thumbStyle, background: props.active ? tokens.bgRowSelected : 'transparent' }}
      title={props.img.name}
      onClick={() => props.onClick()}
    >
      <div style={thumbBoxStyle}>
        <Show when={url()} fallback={<ImageIcon size={22} color={tokens.fgMuted} />}>
          <img src={url()!} alt="" style={{ 'max-width': '100%', 'max-height': '54px', 'object-fit': 'contain' }} />
        </Show>
      </div>
      <span style={thumbNameStyle}>{props.img.name}</span>
    </div>
  );
};

function dirOf(p: string): string {
  const i = p.lastIndexOf('/');
  return i > 0 ? p.slice(0, i) : '/';
}

// ---- styles ----

const listStyle: JSX.CSSProperties = {
  overflow: 'auto',
  background: tokens.bgWindow,
  'border-right': `1px solid ${tokens.borderMenu}`,
};

const thumbStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  'align-items': 'center',
  gap: '3px',
  padding: '6px 4px',
  cursor: 'pointer',
  'user-select': 'none',
};

const thumbBoxStyle: JSX.CSSProperties = {
  width: '100%',
  height: '54px',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
};

const thumbNameStyle: JSX.CSSProperties = {
  'font-size': '10px',
  color: tokens.fg,
  'max-width': '100%',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

const mainStyle: JSX.CSSProperties = {
  position: 'relative',
  display: 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  overflow: 'hidden',
  background: '#0c0c14',
};

const toolbarStyle: JSX.CSSProperties = {
  position: 'absolute',
  bottom: '12px',
  left: '50%',
  transform: 'translateX(-50%)',
  display: 'flex',
  gap: '4px',
  padding: '4px',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusLg}px`,
  'box-shadow': tokens.shadowMenu,
};

const tbBtnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  width: '30px',
  height: '28px',
  background: 'transparent',
  color: tokens.fg,
  border: 'none',
  'border-radius': `${tokens.radiusMd}px`,
  cursor: 'pointer',
};

const captionStyle: JSX.CSSProperties = {
  position: 'absolute',
  top: '8px',
  left: '50%',
  transform: 'translateX(-50%)',
  'font-size': '11px',
  color: tokens.fgMuted,
  background: tokens.bgMenu,
  padding: '2px 10px',
  'border-radius': `${tokens.radiusMd}px`,
  'max-width': '80%',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

defineWashApp('wash-app-imageview', (props) => <App {...props} />, {
  style: `display:grid;grid-template-columns:180px 1fr;width:100%;height:100%;background:#0c0c14;color:${tokens.fg};box-sizing:border-box;overflow:hidden`,
});

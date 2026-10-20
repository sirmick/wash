// wash site: header state, scroll reveals, gallery lightbox, copy buttons.
(() => {
  document.documentElement.classList.add('js');

  const header = document.querySelector('[data-header]');
  const onScroll = () => header.toggleAttribute('data-scrolled', window.scrollY > 12);
  onScroll();
  window.addEventListener('scroll', onScroll, { passive: true });

  const reveal = document.querySelectorAll('[data-reveal]');
  if ('IntersectionObserver' in window) {
    const io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        e.target.classList.add('is-revealed');
        io.unobserve(e.target);
      }
    }, { rootMargin: '0px 0px -8% 0px', threshold: 0.08 });
    reveal.forEach((el) => io.observe(el));
  } else {
    reveal.forEach((el) => el.classList.add('is-revealed'));
  }

  const box = document.querySelector('[data-lightbox]');
  if (box && box.showModal) {
    const img = box.querySelector('img');
    document.querySelectorAll('[data-full]').forEach((btn) => {
      btn.addEventListener('click', () => {
        img.src = btn.dataset.full;
        img.alt = btn.querySelector('img')?.alt || '';
        box.showModal();
      });
    });
    box.addEventListener('click', () => box.close());
  }

  document.querySelectorAll('pre').forEach((pre) => {
    if (!navigator.clipboard) return;
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'copy';
    b.textContent = 'copy';
    b.addEventListener('click', async () => {
      await navigator.clipboard.writeText(pre.querySelector('code').textContent);
      b.textContent = 'copied';
      setTimeout(() => { b.textContent = 'copy'; }, 1400);
    });
    pre.append(b);
  });

  // Live panel: boot the demo VM in a hidden same-origin iframe shortly after
  // the page settles, so by the time you reach the closing section the
  // desktop is already up and simply fades in over the poster. Only on wide
  // screens and when the visitor hasn't asked to save data — it downloads
  // ~13 MiB and keeps an emulator busy in this tab.
  const live = document.querySelector('[data-live]');
  const conn = navigator.connection;
  const eligible = live &&
    window.matchMedia('(min-width: 64rem)').matches &&
    !(conn && conn.saveData) &&
    !window.matchMedia('(prefers-reduced-data: reduce)').matches;
  if (eligible) {
    const status = live.querySelector('[data-live-status]');
    const start = () => {
      live.dataset.booting = '';
      const frame = document.createElement('iframe');
      frame.src = 'demo/?embed';
      frame.title = 'wash live demo';
      // ?embed also stops the demo's own focus() calls scrolling this page
      // (see the inline script in wash-vm/web/index.html).
      live.querySelector('[data-live-frame]').append(frame);
      const t0 = performance.now();
      const secs = () => Math.round((performance.now() - t0) / 1000);
      const doc = () => { try { return frame.contentDocument; } catch { return null; } };
      // Resolve with the first element matching sel inside the demo, or null
      // after ms.
      const waitFor = (sel, ms) => new Promise((resolve) => {
        const end = performance.now() + ms;
        const tick = () => {
          const el = doc()?.querySelector(sel);
          if (el) resolve(el);
          else if (performance.now() > end) resolve(null);
          else setTimeout(tick, 250);
        };
        tick();
      });
      const show = () => {
        live.dataset.ready = '';
        status.textContent = 'Running in this tab. Click into the desktop to use it.';
      };
      // Stage the IDE the way a person would: open Terminal, cd into the
      // sample project baked into the demo image, open main.go in the editor
      // and the folder in Files from the prompt, then tile the three windows.
      // Any step that doesn't show up in time just ends the staging early.
      const stage = async () => {
        status.textContent = 'Opening a project…';
        (await waitFor('button[title="Apps"]', 1000))?.click();
        (await waitFor('[data-testid="start-menu-com.wash.term"]', 5000))?.click();
        const host = await waitFor('wash-app-term [data-testid="term-host"]', 30000);
        if (!host) return;
        await new Promise((r) => setTimeout(r, 1500)); // let the shell print its prompt
        const type = (text) => host.__washTerm?.paste(text);
        type('cd ~/project && ls\n');
        await new Promise((r) => setTimeout(r, 800));
        type('wash-edit main.go\n');
        if (!(await waitFor('wash-app-edit', 30000))) return;
        type('wash-fm .\n');
        if (!(await waitFor('wash-app-fm', 30000))) return;
        tile();
      };
      // Files down the left, Editor top right, Terminal under it, all clear
      // of the sidebar and the taskbar.
      const tile = () => {
        const wash = frame.contentWindow?.wash;
        if (!wash) return;
        const find = (id) => wash.windows().find((w) => w.appID === id);
        const side = doc().querySelector('[data-testid^="sidebar-section-"]')?.getBoundingClientRect().left
          ?? frame.clientWidth * 0.75;
        const g = 12, W = side - g, H = frame.clientHeight - 48;
        const fw = Math.round(W * 0.34), rx = fw + 2 * g, rw = W - rx - g;
        const eh = Math.round((H - 3 * g) * 0.6);
        const place = (id, x, y, w, h) => {
          const win = find(id);
          if (!win) return;
          wash.moveWindow(win.windowID, x, y, win.origin);
          // resizeWindow sizes the content; the title bar sits on top of it.
          const bar = doc().querySelector('.wash-window [data-testid="window-close"]')?.parentElement?.getBoundingClientRect().height ?? 28;
          wash.resizeWindow(win.windowID, w, h - bar, win.origin);
        };
        place('com.wash.fm', g, g, fw, H - 2 * g);
        place('com.wash.edit', rx, g, rw, eh);
        place('com.wash.term', rx, eh + 2 * g, rw, H - eh - 3 * g);
      };
      const boot = setInterval(() => {
        if (doc()?.querySelector('button[title="Apps"]')) {
          clearInterval(boot);
          stage().finally(show);
        } else {
          status.textContent = `Booting Linux in this tab… ${secs()}s`;
        }
      }, 500);
    };
    const later = () => setTimeout(() => (window.requestIdleCallback || setTimeout)(start, { timeout: 3000 }), 1500);
    if (document.readyState === 'complete') later(); else window.addEventListener('load', later, { once: true });
  }
})();

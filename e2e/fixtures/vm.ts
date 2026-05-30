// VM-backed Playwright fixture (docs/NET.md §8.3, §10, B1e-2). Unlike the
// router fixture (which spawns wash-router directly on the host), this boots a
// real Alpine microvm via washvm-run and points the browser at the proxy that
// fronts it. The browser loads the host chrome, the shell connects ws://…/ws,
// and the proxy tunnels to the in-guest wash-router over the serial data plane:
// the wash UI + wire are served BY the VM, exercising the full real stack.
//
// Prereqs (qemu + /dev/kvm + the built image + chrome) gate the suite — call
// vmSkipReason() in a beforeEach and test.skip() when non-null, so the spec is a
// no-op on machines without nested virt instead of a failure.

import { test as base, expect } from '@playwright/test';
import { spawn, ChildProcess, execSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, '..', '..');
const RUN_BIN = join(REPO_ROOT, 'out', 'washvm-run');
const KERNEL = join(REPO_ROOT, 'out', 'vm', 'vmlinuz');
const INITRAMFS = join(REPO_ROOT, 'out', 'vm', 'initramfs.gz');
const CHROME = join(REPO_ROOT, 'web', 'shell', 'dist');

/** Returns a human reason to skip, or null when the VM gate can run here. */
export function vmSkipReason(): string | null {
  if (!existsSync('/dev/kvm')) return '/dev/kvm not available';
  if (!existsSync(RUN_BIN)) return `${RUN_BIN} missing (go build ./cmd/washvm-run)`;
  if (!existsSync(KERNEL) || !existsSync(INITRAMFS)) return 'VM image missing (scripts/build-vm-image-alpine.sh)';
  if (!existsSync(join(CHROME, 'shell.js'))) return 'shell chrome missing (pnpm -F @wash/shell build)';
  try {
    execSync('command -v qemu-system-x86_64', { stdio: 'ignore' });
  } catch {
    return 'qemu-system-x86_64 not found';
  }
  return null;
}

interface VMHandle {
  url: string;
}

export const test = base.extend<{ vm: VMHandle }>({
  vm: async ({}, use) => {
    const proc: ChildProcess = spawn(RUN_BIN, ['--chrome', CHROME, '--addr', '127.0.0.1:0'], {
      cwd: REPO_ROOT,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    let err = '';
    proc.stdout!.on('data', (b) => (out += b.toString()));
    proc.stderr!.on('data', (b) => (err += b.toString()));

    const url = await new Promise<string>((resolveURL, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`washvm-run not ready in 45s\nstdout:\n${out}\nstderr:\n${err}`)),
        45_000,
      );
      const tick = setInterval(() => {
        const m = out.match(/wash-vm ready at (\S+)/);
        if (m) {
          clearTimeout(timer);
          clearInterval(tick);
          resolveURL(m[1]);
        }
        if (proc.exitCode !== null) {
          clearTimeout(timer);
          clearInterval(tick);
          reject(new Error(`washvm-run exited (${proc.exitCode})\nstderr:\n${err}`));
        }
      }, 200);
    });

    await use({ url });

    proc.kill('SIGTERM');
    await new Promise<void>((r) => {
      proc.on('close', () => r());
      setTimeout(r, 5_000);
    });
  },
});

export { expect };

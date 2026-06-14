// Two-VM remote-apps fixture (docs/REMOTE.md R2 capstone). Boots the
// washvm-remote-run harness, which brings up VM-A (the desktop, fronted by
// the chrome proxy) and VM-B (the ssh host) on a shared qemu mcast L2,
// wires ssh trust between them, and prints VM-A's proxy URL. The browser
// loads VM-A; the spec drives wash-connect to SSH into VM-B for real.
//
// Same KVM/qemu/artifact gate as the single-VM fixture, plus the extra
// washvm-remote-run binary — call remoteVmSkipReason() in a beforeEach.

import { test as base, expect } from '@playwright/test';
import { spawn, ChildProcess, execSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, '..', '..');
const RUN_BIN = join(REPO_ROOT, 'out', 'washvm-remote-run');
const KERNEL = join(REPO_ROOT, 'out', 'vm', 'vmlinuz');
const INITRAMFS = join(REPO_ROOT, 'out', 'vm', 'initramfs.gz');
const CHROME = join(REPO_ROOT, 'out', 'vm-chrome');

/** The SSH target the spec types into wash-connect (must match the harness). */
export const REMOTE_HOST = 'wash@10.77.0.2';

/** Returns a human reason to skip, or null when the two-VM gate can run. */
export function remoteVmSkipReason(): string | null {
  if (process.env.WASH_E2E_SKIP_VM === '1') {
    return 'WASH_E2E_SKIP_VM=1 (VM tiers run under make net-test / make e2e-vm)';
  }
  if (!existsSync('/dev/kvm')) return '/dev/kvm not available (VM e2e needs KVM)';
  try {
    execSync('command -v qemu-system-x86_64', { stdio: 'ignore' });
  } catch {
    return 'qemu-system-x86_64 not found (VM e2e needs qemu)';
  }
  if (
    !existsSync(RUN_BIN) ||
    !existsSync(KERNEL) ||
    !existsSync(INITRAMFS) ||
    !existsSync(join(CHROME, 'chrome.js'))
  ) {
    return 'two-VM e2e artifacts not built — run `make e2e-remote-vm`';
  }
  return null;
}

interface RemoteVMHandle {
  url: string;
}

export const test = base.extend<{ remoteVm: RemoteVMHandle }>({
  remoteVm: async ({}, use) => {
    const proc: ChildProcess = spawn(RUN_BIN, ['--chrome', CHROME, '--addr', '127.0.0.1:0'], {
      cwd: REPO_ROOT,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    let err = '';
    proc.stdout!.on('data', (b) => (out += b.toString()));
    proc.stderr!.on('data', (b) => (err += b.toString()));

    // Two VM boots + ssh wiring takes longer than one VM — give it 120s.
    const url = await new Promise<string>((resolveURL, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`washvm-remote-run not ready in 120s\nstdout:\n${out}\nstderr:\n${err}`)),
        120_000,
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
          reject(new Error(`washvm-remote-run exited (${proc.exitCode})\nstderr:\n${err}\nstdout:\n${out}`));
        }
      }, 250);
    });

    await use({ url });

    proc.kill('SIGTERM');
    await new Promise<void>((r) => {
      proc.on('close', () => r());
      setTimeout(r, 8_000);
    });
  },
});

export { expect };

// vmLogin re-exported shape: the in-guest front gates VM-A's channel exactly
// like the single-VM fixture, so the login flow is identical.
export async function vmLogin(page: import('@playwright/test').Page, user = 'wash', pass = 'wash'): Promise<void> {
  await page.locator('#login-user').fill(user, { timeout: 60_000 });
  await page.locator('#login-pass').fill(pass);
  await page.locator('#login-go').click();
}

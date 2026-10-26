import type { SaverHandle, SaverOption } from '@wash/ui';
import { CAMS, LOOKS, PROGRAMS, mount as mountLogo } from '../vendor/logowriter3d.js';
import { SAVER_FPS } from './canvas';

// Each start plays the 16 bundled Logo programs in shuffled order (or just
// one, if chosen), each drawing then dissolving into the next.
export const options: SaverOption[] = [
  {
    key: 'program',
    label: 'Program',
    kind: 'select',
    choices: [['all', 'All, shuffled'], ...PROGRAMS.map((p): [string, string] => [p.id, p.name])],
    default: 'all',
  },
  {
    key: 'look',
    label: 'Look',
    kind: 'select',
    choices: [['random', 'Random'], ...LOOKS.map((l): [string, string] => [l.id, l.name])],
    default: 'random',
  },
  {
    key: 'cam',
    label: 'Camera',
    kind: 'select',
    choices: CAMS.map((c): [string, string] => [c.id, c.id === 'auto' ? 'Per program' : c.name]),
    default: 'auto',
  },
  { key: 'speed', label: 'Speed', kind: 'range', min: 0.25, max: 3, step: 0.25, default: 1 },
];

export function mount(container: HTMLElement, o: Record<string, unknown>): SaverHandle {
  const program = o.program as string;
  return mountLogo(container, {
    programs: program && program !== 'all' ? [program] : 'all',
    order: 'shuffle',
    look: o.look as string,
    cam: o.cam as string,
    speed: o.speed as number,
    fps: SAVER_FPS,
  });
}

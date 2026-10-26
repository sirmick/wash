import type { SaverHandle, SaverOption } from '@wash/ui';
import { Moire, type MoirePalette, type MoirePattern } from '../moire-interference';
import { fillCanvas, SAVER_FPS } from './canvas';

export const options: SaverOption[] = [
  {
    key: 'pattern',
    label: 'Pattern',
    kind: 'select',
    choices: [['auto', 'Cycle'], ['rings', 'Rings'], ['gratings', 'Gratings'], ['spirals', 'Spirals'], ['halftone', 'Halftone']],
    default: 'auto',
  },
  {
    key: 'palette',
    label: 'Palette',
    kind: 'select',
    choices: [['neon', 'Neon'], ['gel', 'Gel'], ['ink', 'Ink'], ['glow', 'Glow']],
    default: 'neon',
  },
  { key: 'brightness', label: 'Brightness', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'density', label: 'Density', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'speed', label: 'Speed', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.4 },
];

export function mount(container: HTMLElement, o: Record<string, unknown>): SaverHandle {
  const canvas = fillCanvas(container);
  try {
    const m = new Moire(canvas, {
      pattern: o.pattern as MoirePattern | 'auto',
      palette: o.palette as MoirePalette,
      brightness: o.brightness as number,
      density: o.density as number,
      speed: o.speed as number,
      fps: SAVER_FPS,
    });
    m.start();
    return { dispose: () => (m.destroy(), canvas.remove()) };
  } catch (e) {
    canvas.remove();
    throw e;
  }
}

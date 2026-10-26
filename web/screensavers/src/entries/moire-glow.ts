import type { SaverHandle, SaverOption } from '@wash/ui';
import { MoireGlow, type GlowPalette, type GlowShape } from '../moire-glow';
import { fillCanvas } from './canvas';

export const options: SaverOption[] = [
  {
    key: 'shape',
    label: 'Shape',
    kind: 'select',
    choices: [['auto', 'Cycle'], ['lines', 'Lines'], ['rects', 'Rectangles'], ['ovals', 'Ovals'], ['polygon', 'Polygon'], ['fan', 'Fan']],
    default: 'auto',
  },
  {
    key: 'palette',
    label: 'Palette',
    kind: 'select',
    choices: [['aurora', 'Aurora'], ['neon', 'Neon'], ['ice', 'Ice'], ['ember', 'Ember'], ['mono', 'Mono']],
    default: 'aurora',
  },
  { key: 'brightness', label: 'Brightness', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'trail', label: 'Trail', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'glow', label: 'Glow', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'speed', label: 'Speed', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.4 },
];

export function mount(container: HTMLElement, o: Record<string, unknown>): SaverHandle {
  const canvas = fillCanvas(container);
  try {
    const m = new MoireGlow(canvas, {
      shape: o.shape as GlowShape | 'auto',
      palette: o.palette as GlowPalette,
      brightness: o.brightness as number,
      trail: o.trail as number,
      glow: o.glow as number,
      speed: o.speed as number,
    });
    m.start();
    m.randomize(); // a fresh starting hue each time
    return { dispose: () => (m.destroy(), canvas.remove()) };
  } catch (e) {
    canvas.remove();
    throw e;
  }
}

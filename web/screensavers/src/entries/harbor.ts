import type { SaverHandle, SaverOption } from '@wash/ui';
import { HarborNight, type BridgeType } from '../harbor-night';
import { fillCanvas, randomSeed, SAVER_FPS } from './canvas';

export const options: SaverOption[] = [
  { key: 'brightness', label: 'Brightness', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.5 },
  { key: 'clouds', label: 'Clouds', kind: 'range', min: 0, max: 1, step: 0.05, default: 0.4 },
  { key: 'fog', label: 'Fog', kind: 'range', min: 0, max: 1, step: 0.05, default: 0 },
  { key: 'speed', label: 'Speed', kind: 'range', min: 0.25, max: 3, step: 0.25, default: 1 },
  {
    key: 'bridge',
    label: 'Bridge',
    kind: 'select',
    choices: [['auto', 'Random'], ['cable', 'Cable-stayed'], ['suspension', 'Suspension'], ['arch', 'Arch'], ['none', 'None']],
    default: 'auto',
  },
];

export function mount(container: HTMLElement, o: Record<string, unknown>): SaverHandle {
  const canvas = fillCanvas(container);
  try {
    const sn = new HarborNight(canvas, {
      brightness: o.brightness as number,
      clouds: o.clouds as number,
      fog: o.fog as number,
      speed: o.speed as number,
      bridge: o.bridge as BridgeType | 'auto',
      seed: randomSeed(),
      fps: SAVER_FPS,
    });
    sn.start();
    return { dispose: () => (sn.destroy(), canvas.remove()) };
  } catch (e) {
    canvas.remove();
    throw e;
  }
}

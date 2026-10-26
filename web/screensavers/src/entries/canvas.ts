// fillCanvas adds a canvas that fills the screensaver container. The effects
// size their backbuffer from the canvas's CSS box (ResizeObserver).
export function fillCanvas(container: HTMLElement): HTMLCanvasElement {
  const canvas = document.createElement('canvas');
  canvas.style.cssText = 'display:block;width:100%;height:100%';
  container.appendChild(canvas);
  return canvas;
}

export const randomSeed = () => (Math.random() * 2 ** 31) | 0;

// A screensaver doesn't need every frame; 30 fps halves the GPU cost.
export const SAVER_FPS = 30;

import { useEffect, useRef } from "react";

type GalaxyCell = {
  x: number;
  y: number;
  size: number;
  position: number;
  presence: number;
  phase: number;
  speed: number;
  cluster: number;
  color: number;
};

export function ChatThinkingGalaxy({ active }: { active: boolean }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!active || !canvas) return;
    const context = canvas.getContext("2d");
    if (!context) return;

    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    let frame = 0;
    let width = 0;
    let height = 0;
    let cells: GalaxyCell[] = [];
    let colors: string[] = [];

    function draw(time: number) {
      if (!context) return;
      context.clearRect(0, 0, width, height);
      for (const cell of cells) {
        const wave = Math.sin(time * 3 - cell.position * 10 + cell.cluster);
        const flicker =
          0.5 + 0.5 * Math.sin(time * cell.speed * 6 + cell.phase);
        const density = 0.16 + 0.8 * Math.pow(cell.position, 1.65);
        const presence =
          1 -
          smoothStep(
            density - 0.09,
            density + 0.1,
            cell.presence + wave * 0.12,
          );
        const brightness =
          (0.18 + 0.82 * Math.pow(cell.position, 0.8)) *
          (0.16 + 0.84 * Math.pow(flicker, 1.2));
        const alpha = presence * brightness;
        if (alpha < 0.02) continue;
        context.globalAlpha = alpha;
        context.fillStyle = colors[cell.color];
        context.beginPath();
        context.roundRect(cell.x, cell.y, cell.size, cell.size, 0.65);
        context.fill();
      }
      context.globalAlpha = 1;
    }

    function animate(timestamp: number) {
      draw(timestamp / 1000);
      frame = requestAnimationFrame(animate);
    }

    function startMotion() {
      cancelAnimationFrame(frame);
      if (!cells.length) return;
      draw(reducedMotion.matches ? 3.1 : performance.now() / 1000);
      if (!reducedMotion.matches) frame = requestAnimationFrame(animate);
    }

    function resize() {
      if (!canvas || !context) return;
      const bounds = canvas.getBoundingClientRect();
      width = bounds.width;
      height = bounds.height;
      if (!width || !height) return;
      const ratio = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.round(width * ratio);
      canvas.height = Math.round(height * ratio);
      context.setTransform(ratio, 0, 0, ratio, 0, 0);
      // Resolve light-dark() through a color property before passing it to Canvas.
      colors = ["violet", "pink", "blue", "ice", "white"].map((name) => {
        canvas.style.color = `var(--thinking-cell-${name})`;
        return getComputedStyle(canvas).color;
      });
      cells = createCells(width, height);
      startMotion();
    }

    const resizeObserver = new ResizeObserver(resize);
    resizeObserver.observe(canvas);
    const themeObserver = new MutationObserver(resize);
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["style", "data-boardly-color-mode"],
    });
    reducedMotion.addEventListener("change", startMotion);
    resize();

    return () => {
      cancelAnimationFrame(frame);
      resizeObserver.disconnect();
      themeObserver.disconnect();
      reducedMotion.removeEventListener("change", startMotion);
    };
  }, [active]);

  return (
    <canvas ref={canvasRef} className="chat-thinking-galaxy" aria-hidden />
  );
}

function createCells(width: number, height: number): GalaxyCell[] {
  const rowPitch = height / 5;
  const columns = Math.max(2, Math.ceil(width / rowPitch));
  const pitch = width / columns;
  return Array.from({ length: columns * 5 }, (_, index) => {
    const column = Math.floor(index / 5);
    const row = index % 5;
    return {
      x: column * pitch + 0.55,
      y: row * rowPitch + 0.55,
      size: Math.min(pitch, rowPitch) - 1.05,
      position: column / (columns - 1),
      presence: seededFraction(index + 11),
      phase: seededFraction(index + 317) * Math.PI * 2,
      speed: 0.65 + seededFraction(index + 53) * 1.1,
      cluster:
        seededFraction(Math.floor(column / 4) * 3 + Math.floor(row / 2)) *
        Math.PI *
        2,
      color: Math.floor(seededFraction(index + 911) * 5),
    };
  });
}

// Keep the same pixel field across renders, resizes and StrictMode mounts.
function seededFraction(seed: number) {
  const value = Math.sin(seed * 127.1 + 311.7) * 43758.5453;
  return value - Math.floor(value);
}

function smoothStep(start: number, end: number, value: number) {
  const progress = Math.max(0, Math.min(1, (value - start) / (end - start)));
  return progress * progress * (3 - 2 * progress);
}

// Imperative canvas renderer for the Wiki graph. The React shell hands it the
// pages and links; everything that changes per frame — the force simulation,
// hover emphasis, drag, pan and zoom — stays here, so no frame re-renders React
// and idle graphs draw nothing.

export type GraphPage = {
  id: string;
  title: string;
  /** Any CSS color, including var() references; resolved against the stage. */
  color: string;
  dimmed: boolean;
};
export type GraphLink = { source: string; target: string };

type Point = { x: number; y: number };
type View = Point & { k: number };
type SimNode = GraphPage & {
  x: number;
  y: number;
  vx: number;
  vy: number;
  fixed: boolean;
  radius: number;
  alpha: number;
  label: number;
  scale: number;
};
type SimLink = { source: SimNode; target: SimNode; alpha: number; lit: number };
type Gesture =
  | { kind: "node"; node: SimNode; start: Point; moved: boolean }
  | { kind: "pan"; start: Point; origin: View; moved: boolean };
type Palette = {
  background: string;
  link: string;
  accent: string;
  text: string;
  muted: string;
  nodes: Map<string, string>;
};

const minZoom = 0.1;
const maxZoom = 1.8;
const dragThreshold = 4;
const labelMaxWidth = 140;
// Cools the simulation from alpha 1 to 0.001 in about 300 ticks.
const alphaDecay = 1 - Math.pow(0.001, 1 / 300);

// Small graphs start on a circle ordered to minimize crossings.
function seedPosition(index: number, count: number): Point {
  if (count === 1) return { x: 0, y: 0 };
  const angle = (index / count) * Math.PI * 2 - Math.PI / 2;
  const radius = Math.min(215, 120 + count * 12);
  return {
    x: Math.cos(angle) * radius,
    y: Math.sin(angle) * radius * 0.84,
  };
}

// Larger graphs start as breadth-first trees fanned out from their
// best-connected pages, so linked pages begin beside each other and the forces
// refine the layout instead of untangling crossed links.
function treeSeeds(ids: string[], neighbors: Map<string, Set<string>>) {
  const degree = (id: string) => neighbors.get(id)!.size;
  const positions = new Map<string, Point>();
  let component = 0;
  for (const root of [...ids].sort((a, b) => degree(b) - degree(a))) {
    if (positions.has(root)) continue;
    const angle = component * Math.PI * (3 - Math.sqrt(5));
    const radius = 150 * Math.sqrt(component++);
    positions.set(root, {
      x: Math.cos(angle) * radius,
      y: Math.sin(angle) * radius,
    });
    const queue = [root];
    for (let head = 0; head < queue.length; head++) {
      const id = queue[head];
      const origin = positions.get(id)!;
      const children = [...neighbors.get(id)!]
        .filter((child) => !positions.has(child))
        .sort((a, b) => degree(b) - degree(a));
      // A root fans children all around it; others fan outward from it.
      const arc = id === root ? Math.PI * 2 : Math.PI;
      const outward = Math.atan2(origin.y, origin.x);
      children.forEach((child, index) => {
        const direction =
          outward - arc / 2 + (arc / children.length) * (index + 0.5);
        positions.set(child, {
          x: origin.x + Math.cos(direction) * 90,
          y: origin.y + Math.sin(direction) * 90,
        });
        queue.push(child);
      });
    }
  }
  return positions;
}

function circularOrder(ids: string[], links: GraphLink[]) {
  if (ids.length < 3) return ids;
  function score(order: string[]) {
    const positions = new Map(order.map((id, index) => [id, index]));
    const between = (value: number, start: number, end: number) =>
      start < end ? value > start && value < end : value > start || value < end;
    let crossings = 0;
    for (let i = 0; i < links.length; i++) {
      const a = positions.get(links[i].source)!;
      const b = positions.get(links[i].target)!;
      for (let j = i + 1; j < links.length; j++) {
        if (
          links[i].source === links[j].source ||
          links[i].source === links[j].target ||
          links[i].target === links[j].source ||
          links[i].target === links[j].target
        )
          continue;
        const c = positions.get(links[j].source)!;
        const d = positions.get(links[j].target)!;
        if (between(c, a, b) !== between(d, a, b)) crossings++;
      }
    }
    const span = links.reduce((total, link) => {
      const distance = Math.abs(
        positions.get(link.source)! - positions.get(link.target)!,
      );
      return total + Math.min(distance, order.length - distance);
    }, 0);
    return crossings * 1000 + span;
  }
  let best = ids;
  let bestScore = score(best);
  if (ids.length <= 8) {
    const visit = (prefix: string[], remaining: string[]) => {
      if (!remaining.length) {
        const candidate = [ids[0], ...prefix];
        const value = score(candidate);
        if (value < bestScore) {
          best = candidate;
          bestScore = value;
        }
        return;
      }
      remaining.forEach((id, index) =>
        visit(
          [...prefix, id],
          remaining.filter((_, other) => other !== index),
        ),
      );
    };
    visit([], ids.slice(1));
  } else if (ids.length <= 12) {
    for (let pass = 0; pass < 4; pass++) {
      let improved = false;
      for (let i = 1; i < ids.length; i++) {
        for (let j = i + 1; j < ids.length; j++) {
          const candidate = [...best];
          [candidate[i], candidate[j]] = [candidate[j], candidate[i]];
          const value = score(candidate);
          if (value < bestScore) {
            best = candidate;
            bestScore = value;
            improved = true;
          }
        }
      }
      if (!improved) break;
    }
  }
  return best;
}

const clamp = (value: number, low: number, high: number) =>
  Math.min(high, Math.max(low, value));

export class WikiGraphScene {
  private readonly context: CanvasRenderingContext2D;
  private nodes: SimNode[] = [];
  private nodeById = new Map<string, SimNode>();
  private links: SimLink[] = [];
  private neighbors = new Map<string, Set<string>>();
  private anchors = new Map<string, Point>();
  private structureKey = "";
  private view: View = { x: 0, y: 0, k: 1 };
  private viewTween: {
    from: View;
    to: View;
    start: number;
    duration: number;
  } | null = null;
  private size = { width: 0, height: 0, ratio: 1 };
  private alpha = 0;
  private alphaTarget = 0;
  private hoverId: string | null = null;
  private focusId: string | null = null;
  private gesture: Gesture | null = null;
  private palette: Palette | null = null;
  private fontFamily = "sans-serif";
  private labelCache = new Map<string, string>();
  private frame = 0;
  private readonly reducedMotion: MediaQueryList;
  private readonly colorScheme: MediaQueryList;
  private readonly resizeObserver: ResizeObserver;
  private readonly themeObserver: MutationObserver;

  constructor(
    private readonly canvas: HTMLCanvasElement,
    private readonly stage: HTMLElement,
    private readonly callbacks: {
      onOpen: (pageId: string) => void;
      onResize: () => void;
    },
  ) {
    this.context = canvas.getContext("2d")!;
    this.reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    this.colorScheme = window.matchMedia("(prefers-color-scheme: dark)");
    this.resizeObserver = new ResizeObserver(() => {
      this.measure();
      this.callbacks.onResize();
    });
    this.resizeObserver.observe(stage);
    this.themeObserver = new MutationObserver(this.invalidatePalette);
    this.themeObserver.observe(document.documentElement, {
      attributes: true,
      // WorkspaceAppearanceProvider writes the palette to the root element.
      attributeFilter: [
        "class",
        "style",
        "data-boardly-workspace-theme",
        "data-boardly-color-mode",
      ],
    });
    this.colorScheme.addEventListener("change", this.invalidatePalette);
    canvas.addEventListener("pointerdown", this.onPointerDown);
    canvas.addEventListener("pointermove", this.onPointerMove);
    canvas.addEventListener("pointerup", this.onPointerUp);
    canvas.addEventListener("pointercancel", this.onPointerCancel);
    canvas.addEventListener("pointerleave", this.onPointerLeave);
    canvas.addEventListener("wheel", this.onWheel, { passive: false });
    this.measure();
  }

  destroy() {
    cancelAnimationFrame(this.frame);
    this.resizeObserver.disconnect();
    this.themeObserver.disconnect();
    this.colorScheme.removeEventListener("change", this.invalidatePalette);
    this.canvas.removeEventListener("pointerdown", this.onPointerDown);
    this.canvas.removeEventListener("pointermove", this.onPointerMove);
    this.canvas.removeEventListener("pointerup", this.onPointerUp);
    this.canvas.removeEventListener("pointercancel", this.onPointerCancel);
    this.canvas.removeEventListener("pointerleave", this.onPointerLeave);
    this.canvas.removeEventListener("wheel", this.onWheel);
  }

  /**
   * Replaces the graph. Titles, colors and search dimming apply in place; only
   * a change to which pages or links exist moves nodes, and existing nodes keep
   * their positions so a realtime refetch never reshuffles the layout.
   */
  setGraph(pages: GraphPage[], links: GraphLink[]) {
    const key = `${pages.map((page) => page.id).join("\n")}\u0000${links
      .map((link) => `${link.source}→${link.target}`)
      .join("\n")}`;
    if (key !== this.structureKey) {
      this.rebuild(pages, links);
      this.structureKey = key;
    }
    for (const page of pages) {
      const node = this.nodeById.get(page.id)!;
      node.title = page.title;
      node.color = page.color;
      node.dimmed = page.dimmed;
    }
    this.requestFrame();
  }

  /** Keyboard focus highlights a node like hover and scrolls it into view. */
  setFocus(pageId: string | null) {
    this.focusId = pageId;
    const node = pageId ? this.nodeById.get(pageId) : undefined;
    if (node) {
      const screen = this.toScreen(node);
      const margin = 40;
      if (
        screen.x < margin ||
        screen.y < margin ||
        screen.x > this.size.width - margin ||
        screen.y > this.size.height - margin
      ) {
        this.centerOn(pageId!, this.view.k, 220);
      }
    }
    this.requestFrame();
  }

  /** Returns every node to its seeded layout and lets the forces settle again. */
  rearrange() {
    for (const node of this.nodes) {
      const anchor = this.anchors.get(node.id)!;
      node.x = anchor.x;
      node.y = anchor.y;
      node.vx = 0;
      node.vy = 0;
    }
    this.heat(1);
  }

  zoomBy(factor: number, duration: number) {
    const center = { x: this.size.width / 2, y: this.size.height / 2 };
    this.animateView(this.zoomedAt(center, this.view.k * factor), duration);
  }

  fit(duration: number) {
    if (!this.nodes.length || !this.size.width) return;
    let left = Infinity;
    let top = Infinity;
    let right = -Infinity;
    let bottom = -Infinity;
    for (const node of this.nodes) {
      left = Math.min(left, node.x - node.radius);
      top = Math.min(top, node.y - node.radius);
      right = Math.max(right, node.x + node.radius);
      bottom = Math.max(bottom, node.y + node.radius);
    }
    const usable = 1 - 2 * 0.12;
    const k = clamp(
      Math.min(
        1,
        (this.size.width * usable) / Math.max(1, right - left),
        (this.size.height * usable) / Math.max(1, bottom - top),
      ),
      minZoom,
      maxZoom,
    );
    this.animateView(
      {
        k,
        x: this.size.width / 2 - ((left + right) / 2) * k,
        y: this.size.height / 2 - ((top + bottom) / 2) * k,
      },
      duration,
    );
  }

  centerOn(pageId: string, zoom: number, duration: number) {
    const node = this.nodeById.get(pageId);
    if (!node) return;
    const k = clamp(zoom, minZoom, maxZoom);
    this.animateView(
      {
        k,
        x: this.size.width / 2 - node.x * k,
        y: this.size.height / 2 - node.y * k,
      },
      duration,
    );
  }

  private rebuild(pages: GraphPage[], links: GraphLink[]) {
    const ids = pages.map((page) => page.id);
    this.neighbors = new Map(ids.map((id) => [id, new Set<string>()]));
    for (const link of links) {
      this.neighbors.get(link.source)!.add(link.target);
      this.neighbors.get(link.target)!.add(link.source);
    }
    if (ids.length > 12) {
      this.anchors = treeSeeds(ids, this.neighbors);
    } else {
      const order = circularOrder(ids, links);
      this.anchors = new Map(
        order.map((id, index) => [id, seedPosition(index, order.length)]),
      );
    }
    const previous = this.nodeById;
    const fresh = previous.size === 0;
    this.nodes = pages.map((page) => {
      const existing = previous.get(page.id);
      if (existing) return existing;
      // A page added to a settled graph starts beside a page it links to.
      const neighbor = fresh
        ? undefined
        : [...this.neighbors.get(page.id)!]
            .map((id) => previous.get(id))
            .find(Boolean);
      const anchor = this.anchors.get(page.id)!;
      return {
        ...page,
        x: neighbor ? neighbor.x + (Math.random() - 0.5) * 30 : anchor.x,
        y: neighbor ? neighbor.y + (Math.random() - 0.5) * 30 : anchor.y,
        vx: 0,
        vy: 0,
        fixed: false,
        radius: 4,
        alpha: 1,
        label: 0,
        scale: 1,
      };
    });
    this.nodeById = new Map(this.nodes.map((node) => [node.id, node]));
    for (const node of this.nodes) {
      node.radius =
        4 + Math.min(4, Math.sqrt(this.neighbors.get(node.id)!.size) * 1.2);
    }
    const previousLinks = new Map(
      this.links.map((link) => [`${link.source.id}→${link.target.id}`, link]),
    );
    this.links = links.map(
      (link) =>
        previousLinks.get(`${link.source}→${link.target}`) ?? {
          source: this.nodeById.get(link.source)!,
          target: this.nodeById.get(link.target)!,
          alpha: 0.4,
          lit: 0,
        },
    );
    if (this.hoverId && !this.nodeById.has(this.hoverId)) this.hoverId = null;
    if (this.focusId && !this.nodeById.has(this.focusId)) this.focusId = null;
    if (fresh) {
      // Settle most of the layout before the first paint so the graph opens
      // composed; the remainder animates.
      this.alpha = 1;
      const warmup = this.reducedMotion.matches ? 300 : 40;
      for (let tick = 0; tick < warmup && this.alpha > 0.001; tick++) {
        this.step();
      }
      this.heat(this.alpha);
    } else {
      this.heat(0.5);
    }
  }

  private heat(alpha: number) {
    this.alpha = Math.max(this.alpha, alpha);
    if (this.reducedMotion.matches) {
      while (this.alpha > 0.001 && this.alphaTarget === 0) this.step();
    }
    this.requestFrame();
  }

  /** One force-simulation tick; returns the fastest node's speed. */
  private step() {
    const nodes = this.nodes;
    const alpha = this.alpha;
    const extent = Math.max(300, 39 * Math.sqrt(nodes.length) + 90);
    for (let i = 0; i < nodes.length; i++) {
      const left = nodes[i];
      for (let j = i + 1; j < nodes.length; j++) {
        const right = nodes[j];
        const dx = right.x - left.x || 0.01;
        const dy = right.y - left.y || 0.01;
        const distance = Math.hypot(dx, dy);
        const force =
          (Math.min(1.7, 2400 / (distance * distance)) +
            Math.max(0, 145 - distance) * 0.025) *
          alpha;
        const fx = (dx / distance) * force;
        const fy = (dy / distance) * force;
        left.vx -= fx;
        left.vy -= fy;
        right.vx += fx;
        right.vy += fy;
      }
    }
    for (const { source, target } of this.links) {
      const dx = target.x - source.x || 0.01;
      const dy = target.y - source.y || 0.01;
      const distance = Math.hypot(dx, dy);
      const force = (distance - 165) * 0.008 * alpha;
      const fx = (dx / distance) * force;
      const fy = (dy / distance) * force;
      source.vx += fx;
      source.vy += fy;
      target.vx -= fx;
      target.vy -= fy;
    }
    let maxSpeed = 0;
    for (const node of nodes) {
      if (node.fixed) {
        node.vx = 0;
        node.vy = 0;
        continue;
      }
      node.vx -= node.x * 0.003 * alpha;
      node.vy -= node.y * 0.003 * alpha;
      // Ease nodes toward a circular boundary instead of clamping each axis,
      // which piles nodes onto four flat sides in larger graphs.
      const radius = Math.hypot(node.x, node.y);
      if (radius > extent * 0.8) {
        const inward = (radius - extent * 0.8) * 0.08 * alpha;
        node.vx -= (node.x / radius) * inward;
        node.vy -= (node.y / radius) * inward;
      }
      node.vx *= 0.78;
      node.vy *= 0.78;
      node.x += node.vx;
      node.y += node.vy;
      maxSpeed = Math.max(maxSpeed, Math.hypot(node.vx, node.vy));
    }
    this.alpha += (this.alphaTarget - this.alpha) * alphaDecay;
    if (this.alphaTarget === 0 && maxSpeed < 0.02 && this.alpha < 0.3) {
      this.alpha = 0;
    }
    return maxSpeed;
  }

  private requestFrame() {
    if (!this.frame) this.frame = requestAnimationFrame(this.render);
  }

  private readonly render = (now: number) => {
    this.frame = 0;
    let busy = false;
    if (this.alpha > 0.001 || this.alphaTarget > 0) {
      this.step();
      busy = true;
    } else {
      this.alpha = 0;
    }
    busy = this.advanceView(now) || busy;
    busy = this.advanceEmphasis() || busy;
    this.draw();
    if (busy) this.requestFrame();
  };

  private activeId() {
    const gesture = this.gesture;
    if (gesture?.kind === "node") return gesture.node.id;
    return this.hoverId ?? this.focusId;
  }

  /** Eases hover, search and zoom emphasis; returns whether any is moving. */
  private advanceEmphasis() {
    const active = this.activeId();
    const related = active ? this.neighbors.get(active) : undefined;
    const zoomLabel = clamp((this.view.k - 0.6) / 0.2, 0, 1);
    const instant = this.reducedMotion.matches;
    let moving = false;
    const approach = (current: number, target: number) => {
      if (instant) return target;
      const next = current + (target - current) * 0.24;
      if (Math.abs(target - next) < 0.004) return target;
      moving = true;
      return next;
    };
    for (const node of this.nodes) {
      const isActive = node.id === active;
      const inFocus = isActive || Boolean(related?.has(node.id));
      node.alpha = approach(
        node.alpha,
        node.dimmed || (active && !inFocus) ? 0.22 : 1,
      );
      node.label = approach(node.label, active && inFocus ? 1 : zoomLabel);
      node.scale = approach(node.scale, isActive ? 1.35 : 1);
    }
    for (const link of this.links) {
      const lit = link.source.id === active || link.target.id === active;
      link.lit = approach(link.lit, lit ? 1 : 0);
      link.alpha = approach(link.alpha, active ? (lit ? 0.9 : 0.1) : 0.4);
    }
    return moving;
  }

  private draw() {
    const { width, height, ratio } = this.size;
    if (!width || !height) return;
    const palette = this.resolvePalette();
    const ctx = this.context;
    const { k } = this.view;
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    ctx.clearRect(0, 0, width, height);
    ctx.lineCap = "round";
    for (const link of this.links) {
      if (link.alpha < 0.01) continue;
      const from = this.toScreen(link.source);
      const to = this.toScreen(link.target);
      ctx.globalAlpha = link.alpha;
      ctx.strokeStyle = link.lit > 0.5 ? palette.accent : palette.link;
      ctx.lineWidth = 1 + 0.55 * link.lit;
      ctx.beginPath();
      ctx.moveTo(from.x, from.y);
      ctx.lineTo(to.x, to.y);
      ctx.stroke();
    }
    const active = this.activeId();
    const activeNode = active ? this.nodeById.get(active) : undefined;
    for (const node of this.nodes) {
      if (node !== activeNode) this.drawNode(node, palette);
    }
    if (activeNode) this.drawNode(activeNode, palette);
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    for (const node of this.nodes) {
      const opacity = node.label * node.alpha;
      if (opacity < 0.01) continue;
      const isActive = node === activeNode;
      const font = `${isActive ? 600 : 500} 11px ${this.fontFamily}`;
      if (ctx.font !== font) ctx.font = font;
      const screen = this.toScreen(node);
      ctx.globalAlpha = opacity;
      ctx.fillStyle = isActive ? palette.text : palette.muted;
      ctx.fillText(
        this.fitLabel(node.title, font),
        screen.x,
        screen.y + Math.max(2, node.radius * k) * node.scale + 8,
      );
    }
    ctx.globalAlpha = 1;
  }

  private drawNode(node: SimNode, palette: Palette) {
    const ctx = this.context;
    const screen = this.toScreen(node);
    const radius = Math.max(2, node.radius * this.view.k) * node.scale;
    const color = palette.nodes.get(node.color) ?? this.resolveNodeColor(node);
    if (node.scale > 1.001) {
      ctx.globalAlpha = node.alpha * 0.18 * ((node.scale - 1) / 0.35);
      ctx.fillStyle = color;
      ctx.beginPath();
      ctx.arc(screen.x, screen.y, radius + 7, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = node.alpha;
    ctx.fillStyle = palette.background;
    ctx.beginPath();
    ctx.arc(screen.x, screen.y, radius + 2, 0, Math.PI * 2);
    ctx.fill();
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(screen.x, screen.y, radius, 0, Math.PI * 2);
    ctx.fill();
  }

  private fitLabel(title: string, font: string) {
    const key = `${font}\u0000${title}`;
    const cached = this.labelCache.get(key);
    if (cached !== undefined) return cached;
    const ctx = this.context;
    let label = title;
    if (ctx.measureText(label).width > labelMaxWidth) {
      let low = 0;
      let high = title.length;
      while (low < high) {
        const middle = Math.ceil((low + high) / 2);
        if (
          ctx.measureText(`${title.slice(0, middle)}…`).width <= labelMaxWidth
        )
          low = middle;
        else high = middle - 1;
      }
      label = `${title.slice(0, low).trimEnd()}…`;
    }
    this.labelCache.set(key, label);
    return label;
  }

  // Canvas cannot read custom properties, so theme colors resolve through a
  // probe element inside the stage and re-resolve when the theme changes.
  private resolvePalette() {
    if (this.palette) return this.palette;
    const probe = document.createElement("span");
    probe.style.display = "none";
    this.stage.append(probe);
    const read = (value: string) => {
      probe.style.color = value;
      return getComputedStyle(probe).color;
    };
    this.palette = {
      background: read("var(--workspace-content-bg)"),
      link: read("var(--workspace-native-text-faint)"),
      accent: read("var(--workspace-native-accent)"),
      text: read("var(--wk-text)"),
      muted: read("var(--wk-muted)"),
      nodes: new Map(),
    };
    probe.remove();
    this.fontFamily = getComputedStyle(this.stage).fontFamily || "sans-serif";
    this.labelCache.clear();
    return this.palette;
  }

  private resolveNodeColor(node: SimNode) {
    const probe = document.createElement("span");
    probe.style.display = "none";
    probe.style.color = node.color;
    this.stage.append(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    this.palette!.nodes.set(node.color, color);
    return color;
  }

  private readonly invalidatePalette = () => {
    this.palette = null;
    this.requestFrame();
  };

  private measure() {
    const { width, height } = this.stage.getBoundingClientRect();
    const ratio = window.devicePixelRatio || 1;
    // The first paint centers the layout origin; the shell's fit then settles
    // the zoom.
    if (!this.size.width) this.view = { x: width / 2, y: height / 2, k: 1 };
    this.size = { width, height, ratio };
    this.canvas.width = Math.round(width * ratio);
    this.canvas.height = Math.round(height * ratio);
    this.requestFrame();
  }

  private toScreen(point: Point): Point {
    return {
      x: point.x * this.view.k + this.view.x,
      y: point.y * this.view.k + this.view.y,
    };
  }

  private toWorld(point: Point): Point {
    return {
      x: (point.x - this.view.x) / this.view.k,
      y: (point.y - this.view.y) / this.view.k,
    };
  }

  private zoomedAt(point: Point, zoom: number): View {
    const k = clamp(zoom, minZoom, maxZoom);
    const world = this.toWorld(point);
    return { k, x: point.x - world.x * k, y: point.y - world.y * k };
  }

  private animateView(target: View, duration: number) {
    if (duration <= 0 || this.reducedMotion.matches) {
      this.viewTween = null;
      this.view = target;
    } else {
      this.viewTween = {
        from: { ...this.view },
        to: target,
        start: performance.now(),
        duration,
      };
    }
    this.requestFrame();
  }

  private advanceView(now: number) {
    const tween = this.viewTween;
    if (!tween) return false;
    const progress = clamp((now - tween.start) / tween.duration, 0, 1);
    const eased = 1 - Math.pow(1 - progress, 3);
    const mix = (from: number, to: number) => from + (to - from) * eased;
    this.view = {
      x: mix(tween.from.x, tween.to.x),
      y: mix(tween.from.y, tween.to.y),
      k: mix(tween.from.k, tween.to.k),
    };
    if (progress === 1) this.viewTween = null;
    return progress < 1;
  }

  private pointerPoint(event: PointerEvent | WheelEvent): Point {
    const bounds = this.canvas.getBoundingClientRect();
    return { x: event.clientX - bounds.left, y: event.clientY - bounds.top };
  }

  private hitTest(point: Point) {
    const world = this.toWorld(point);
    let best: SimNode | undefined;
    let bestDistance = Infinity;
    for (const node of this.nodes) {
      const distance = Math.hypot(node.x - world.x, node.y - world.y);
      // Small or far-zoomed dots keep an 8px screen target.
      const reach = Math.max(node.radius + 3, 8 / this.view.k);
      if (distance <= reach && distance < bestDistance) {
        best = node;
        bestDistance = distance;
      }
    }
    return best;
  }

  private setHover(node: SimNode | undefined) {
    const id = node?.id ?? null;
    if (id !== this.hoverId) {
      this.hoverId = id;
      this.requestFrame();
    }
    this.updateCursor();
  }

  private updateCursor() {
    const gesture = this.gesture;
    this.canvas.style.cursor = gesture?.moved
      ? "grabbing"
      : gesture?.kind === "node" || this.hoverId
        ? "pointer"
        : "grab";
  }

  private readonly onPointerDown = (event: PointerEvent) => {
    if (event.button !== 0) return;
    this.canvas.setPointerCapture(event.pointerId);
    const point = this.pointerPoint(event);
    const node = this.hitTest(point);
    this.viewTween = null;
    this.gesture = node
      ? { kind: "node", node, start: point, moved: false }
      : { kind: "pan", start: point, origin: { ...this.view }, moved: false };
    this.updateCursor();
    this.requestFrame();
  };

  private readonly onPointerMove = (event: PointerEvent) => {
    const point = this.pointerPoint(event);
    const gesture = this.gesture;
    if (!gesture) {
      this.setHover(this.hitTest(point));
      return;
    }
    if (
      !gesture.moved &&
      Math.hypot(point.x - gesture.start.x, point.y - gesture.start.y) >=
        dragThreshold
    ) {
      gesture.moved = true;
      this.updateCursor();
    }
    if (!gesture.moved) return;
    if (gesture.kind === "pan") {
      this.view = {
        ...gesture.origin,
        x: gesture.origin.x + point.x - gesture.start.x,
        y: gesture.origin.y + point.y - gesture.start.y,
      };
    } else {
      // The dragged node leads; its neighbors follow through the springs.
      const world = this.toWorld(point);
      gesture.node.fixed = true;
      gesture.node.x = world.x;
      gesture.node.y = world.y;
      this.alphaTarget = 0.3;
      this.alpha = Math.max(this.alpha, 0.3);
    }
    this.requestFrame();
  };

  private endGesture(event: PointerEvent, completed: boolean) {
    const gesture = this.gesture;
    if (!gesture) return;
    this.gesture = null;
    if (this.canvas.hasPointerCapture(event.pointerId)) {
      this.canvas.releasePointerCapture(event.pointerId);
    }
    if (gesture.kind === "node") {
      gesture.node.fixed = false;
      this.alphaTarget = 0;
      if (completed && !gesture.moved) this.callbacks.onOpen(gesture.node.id);
    }
    this.setHover(
      completed ? this.hitTest(this.pointerPoint(event)) : undefined,
    );
    this.requestFrame();
  }

  private readonly onPointerUp = (event: PointerEvent) =>
    this.endGesture(event, true);

  private readonly onPointerCancel = (event: PointerEvent) =>
    this.endGesture(event, false);

  private readonly onPointerLeave = () => {
    if (!this.gesture) this.setHover(undefined);
  };

  private readonly onWheel = (event: WheelEvent) => {
    event.preventDefault();
    this.viewTween = null;
    const lines = event.deltaMode === WheelEvent.DOM_DELTA_LINE ? 16 : 1;
    // Pinch gestures arrive as ctrlKey wheels with small deltas.
    const speed = event.ctrlKey ? 0.01 : 0.0015;
    const factor = Math.exp(-event.deltaY * lines * speed);
    this.view = this.zoomedAt(this.pointerPoint(event), this.view.k * factor);
    this.requestFrame();
  };
}

// Vitest setup for the P8 Task 5 React Flow spike.
//
// jsdom has no layout engine and ships none of the observer / matrix / media
// query APIs that `@xyflow/react` uses. The polyfills below are intentionally
// scoped to React Flow's class names so they cannot change the layout maths of
// unrelated component tests.
//
// Dimensions mirror `ChainCanvasSpike` (canvas 800x600, node 180x48, handle 8).
const REACT_FLOW_CANVAS = { width: 800, height: 600 }
const REACT_FLOW_NODE = { width: 180, height: 48 }
const REACT_FLOW_HANDLE = 8

function reactFlowSize(element: HTMLElement): { width: number; height: number } {
  if (element.classList.contains("react-flow")) return REACT_FLOW_CANVAS
  if (element.classList.contains("react-flow__node")) return REACT_FLOW_NODE
  if (element.classList.contains("react-flow__handle")) {
    return { width: REACT_FLOW_HANDLE, height: REACT_FLOW_HANDLE }
  }
  return { width: 0, height: 0 }
}

function reactFlowOffsetWidth(this: HTMLElement): number {
  return reactFlowSize(this).width
}

function reactFlowOffsetHeight(this: HTMLElement): number {
  return reactFlowSize(this).height
}

// jsdom always reports offsetWidth/offsetHeight as 0. React Flow measures the
// container and every node with these two properties.
Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
  configurable: true,
  get: reactFlowOffsetWidth,
})
Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
  configurable: true,
  get: reactFlowOffsetHeight,
})

class ResizeObserverPolyfill implements ResizeObserver {
  private readonly callback: ResizeObserverCallback
  private readonly pending = new Set<Element>()
  private flushScheduled = false

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
  }

  observe(target: Element): void {
    this.pending.add(target)
    if (this.flushScheduled) return
    this.flushScheduled = true
    // Batch every observe() call of the current tick into one callback, just
    // like a browser observer would, so a 200-node graph is one store update.
    queueMicrotask(() => {
      this.flushScheduled = false
      const targets = [...this.pending].filter((target) => target.isConnected)
      this.pending.clear()
      if (targets.length === 0) return
      const entries = targets.map(
        (target) =>
          ({
            target,
            contentRect: target.getBoundingClientRect(),
            borderBoxSize: [],
            contentBoxSize: [],
            devicePixelContentBoxSize: [],
          }) satisfies ResizeObserverEntry,
      )
      this.callback(entries, this)
    })
  }

  unobserve(target: Element): void {
    this.pending.delete(target)
  }

  disconnect(): void {
    this.pending.clear()
  }
}

class DOMMatrixReadOnlyPolyfill {
  readonly m22: number

  constructor(transform?: string) {
    const value = transform ?? ""
    const matrix = /matrix\(([^)]+)\)/.exec(value)
    if (matrix !== null) {
      const values = matrix[1].split(",").map((part) => Number(part.trim()))
      const m22 = values[5] ?? 1
      this.m22 = Number.isFinite(m22) ? m22 : 1
      return
    }
    const scale = /scale\(([^)]+)\)/.exec(value)
    const parsed = Number(scale?.[1].split(",")[0]?.trim() ?? "1")
    this.m22 = Number.isFinite(parsed) ? parsed : 1
  }
}

globalThis.ResizeObserver = ResizeObserverPolyfill
Object.defineProperty(window, "DOMMatrixReadOnly", {
  configurable: true,
  writable: true,
  value: DOMMatrixReadOnlyPolyfill,
})

if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })
}

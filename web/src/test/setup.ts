// 全局测试前置（仅单测，不参与构建产物）。

// jsdom 没有实现 window.scrollTo()，而路由的 scrollBehavior 每次导航都会调用它，
// 于是每个用例都会打印一条 "Not implemented: Window's scrollTo() method"。
// 这里换成空实现，保持测试输出干净（不影响被测逻辑）。
window.scrollTo = (() => undefined) as typeof window.scrollTo

// jsdom 也没有 EventSource：概览页的 SSE 直播会直接 new 它。
// 单测不验证事件流本身，给一个能订阅、能关闭的空实现即可。
class StubEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  readonly url: string
  readyState = StubEventSource.CONNECTING
  onopen: ((this: EventSource, event: Event) => unknown) | null = null
  onerror: ((this: EventSource, event: Event) => unknown) | null = null
  onmessage: ((this: EventSource, event: MessageEvent) => unknown) | null = null

  constructor(url: string | URL) {
    this.url = String(url)
  }

  addEventListener(): void {}
  removeEventListener(): void {}
  close(): void {
    this.readyState = StubEventSource.CLOSED
  }
}

if (typeof globalThis.EventSource === 'undefined') {
  globalThis.EventSource = StubEventSource as unknown as typeof EventSource
}

// 图表组件用 ResizeObserver 量尺寸；jsdom 没有它 -> 给个永不回调的空壳。
class StubResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = StubResizeObserver as unknown as typeof ResizeObserver
}

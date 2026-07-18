import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll, vi } from 'vitest'
import { server } from './server'

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  sessionStorage.clear()
})
afterAll(() => server.close())

class EventSourceStub extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2
  readyState = EventSourceStub.OPEN
  url: string
  withCredentials = true
  onerror = null
  onmessage = null
  onopen = null

  constructor(url: string | URL) {
    super()
    this.url = String(url)
  }

  close() {
    this.readyState = EventSourceStub.CLOSED
  }
}

vi.stubGlobal('EventSource', EventSourceStub)

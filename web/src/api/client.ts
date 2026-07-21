import { useUI } from '../state'

export interface ProblemDetails {
  type?: string
  title: string
  status: number
  detail?: string
  instance?: string
  requestId?: string
}

export class APIError extends Error {
  constructor(public readonly problem: ProblemDetails) {
    super(problem.detail || problem.title)
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const csrfToken = useUI.getState().csrfToken
  const headers = new Headers(init?.headers)
  headers.set('Accept', 'application/json')
  if (init?.body) headers.set('Content-Type', 'application/json')
  if (csrfToken && init?.method && init.method !== 'GET') {
    headers.set('X-CSRF-Token', csrfToken)
  }
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    headers,
    credentials: 'same-origin',
  })
  if (!response.ok) {
    const fallback: ProblemDetails = {
      title: response.statusText || 'Request failed',
      status: response.status,
    }
    const problem = (await response
      .json()
      .catch(() => fallback)) as ProblemDetails
    throw new APIError(problem)
  }
  if (response.status === 204) return undefined as T
  const content = await response.text()
  if (!content) return undefined as T
  return JSON.parse(content) as T
}

export interface ListResponse<T = Record<string, unknown>> {
  items: T[]
  nextCursor?: string
}

export function jsonBody(value: unknown): RequestInit {
  return { body: JSON.stringify(value) }
}

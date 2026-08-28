import type { Worker } from './workerTypes'

export function confirmAction(message: string): boolean {
  try {
    if (typeof window.confirm !== 'function') return true
    return window.confirm(message) !== false
  } catch {
    return true
  }
}

export function visibleWorkerRoles(worker: Worker): string {
  return worker.roles.filter((role) => role !== 'github').join('、') || '无角色'
}

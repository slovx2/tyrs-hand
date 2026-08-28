import { useOutletContext } from 'react-router'
import type { Worker } from './workerTypes'

export interface WorkerDetailContext {
  worker: Worker
  isAdmin: boolean
}

export function useWorkerDetail(): WorkerDetailContext {
  return useOutletContext<WorkerDetailContext>()
}

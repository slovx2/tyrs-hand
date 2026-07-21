import {
  MutationCache,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { App } from './App'
import { ToastViewport } from './components/ToastViewport'
import { useUI } from './state'
import './styles.css'

const queryClient = new QueryClient({
  mutationCache: new MutationCache({
    onError: (error) => {
      useUI
        .getState()
        .showToast('error', error instanceof Error ? error.message : '操作失败')
    },
  }),
  defaultOptions: {
    queries: { staleTime: 15_000, retry: 1, refetchOnWindowFocus: false },
    mutations: { retry: false },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
        <ToastViewport />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)

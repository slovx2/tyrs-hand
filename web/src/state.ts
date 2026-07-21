import { create } from 'zustand'
import { createJSONStorage, persist } from 'zustand/middleware'

export type Locale = 'zh-CN' | 'en-US'
export type Theme = 'light' | 'dark'
export type ToastType = 'success' | 'error' | 'warning' | 'info'

export interface ToastMessage {
  id: string
  type: ToastType
  message: string
  duration: number
}

const toastDurations: Record<ToastType, number> = {
  success: 3_000,
  error: 5_000,
  warning: 4_000,
  info: 3_500,
}

let toastSequence = 0

interface UIState {
  locale: Locale
  theme: Theme
  csrfToken?: string
  toasts: ToastMessage[]
  setLocale: (locale: Locale) => void
  setTheme: (theme: Theme) => void
  setCSRFToken: (csrfToken?: string) => void
  showToast: (type: ToastType, message: string, duration?: number) => string
  dismissToast: (id: string) => void
  clearToasts: () => void
}

export const useUI = create<UIState>()(
  persist(
    (set) => ({
      locale: 'zh-CN',
      theme: 'light',
      toasts: [],
      setLocale: (locale) => set({ locale }),
      setTheme: (theme) => set({ theme }),
      setCSRFToken: (csrfToken) => set({ csrfToken }),
      showToast: (type, message, requestedDuration) => {
        const id = `toast-${++toastSequence}`
        const duration = requestedDuration ?? toastDurations[type]
        set((state) => ({
          toasts: [...state.toasts.slice(-4), { id, type, message, duration }],
        }))
        globalThis.setTimeout(() => {
          set((state) => ({
            toasts: state.toasts.filter((toast) => toast.id !== id),
          }))
        }, duration)
        return id
      },
      dismissToast: (id) =>
        set((state) => ({
          toasts: state.toasts.filter((toast) => toast.id !== id),
        })),
      clearToasts: () => set({ toasts: [] }),
    }),
    {
      name: 'tyrs-hand-ui',
      storage: createJSONStorage(() => sessionStorage),
      partialize: ({ locale, theme, csrfToken }) => ({
        locale,
        theme,
        csrfToken,
      }),
    },
  ),
)

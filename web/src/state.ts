import { create } from 'zustand'
import { createJSONStorage, persist } from 'zustand/middleware'

export type Locale = 'zh-CN' | 'en-US'
export type Theme = 'light' | 'dark'

interface UIState {
  locale: Locale
  theme: Theme
  csrfToken?: string
  setLocale: (locale: Locale) => void
  setTheme: (theme: Theme) => void
  setCSRFToken: (csrfToken?: string) => void
}

export const useUI = create<UIState>()(
  persist(
    (set) => ({
      locale: 'zh-CN',
      theme: 'light',
      setLocale: (locale) => set({ locale }),
      setTheme: (theme) => set({ theme }),
      setCSRFToken: (csrfToken) => set({ csrfToken }),
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

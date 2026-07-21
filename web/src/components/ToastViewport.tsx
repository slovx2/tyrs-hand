import { useUI, type ToastType } from '../state'

const labels: Record<ToastType, string> = {
  success: '成功',
  error: '错误',
  warning: '提醒',
  info: '提示',
}

const icons: Record<ToastType, string> = {
  success: '✓',
  error: '×',
  warning: '!',
  info: 'i',
}

export function ToastViewport() {
  const toasts = useUI((state) => state.toasts)
  const dismissToast = useUI((state) => state.dismissToast)

  return (
    <div
      className="toast-viewport"
      aria-live="polite"
      aria-atomic="false"
      aria-label="通知"
    >
      {toasts.map((toast) => (
        <div
          className={`toast-card toast-${toast.type}`}
          role={toast.type === 'error' ? 'alert' : 'status'}
          key={toast.id}
        >
          <div className="toast-body">
            <span className="toast-icon" aria-hidden="true">
              {icons[toast.type]}
            </span>
            <div className="toast-content">
              <p className="toast-title">{labels[toast.type]}</p>
              <p className="toast-message">{toast.message}</p>
            </div>
            <button
              type="button"
              className="toast-close"
              aria-label={`关闭${labels[toast.type]}通知`}
              onClick={() => dismissToast(toast.id)}
            >
              ×
            </button>
          </div>
          <div className="toast-track" aria-hidden="true">
            <div
              className="toast-progress"
              style={{ animationDuration: `${toast.duration}ms` }}
            />
          </div>
        </div>
      ))}
    </div>
  )
}

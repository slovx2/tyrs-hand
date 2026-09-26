// 某一资源已丢失不能阻止其余真实进程退出、日志落盘与临时凭据清理。
export async function cleanupManaged(groups) {
  const errors = []
  for (const [group, entries] of Object.entries(groups)) {
    for (const [index, managed] of [...entries].reverse().entries()) {
      try { await managed.stop() }
      catch (error) {
        errors.push({ group, name: managed.name ?? `${group}-${entries.length - index - 1}`,
          error: error instanceof Error ? error.message : String(error) })
      }
    }
  }
  return errors
}

export function completionError(primary, cleanupErrors) {
  if (primary) return primary
  if (cleanupErrors.length) return new AggregateError(
    cleanupErrors.map((entry) => new Error(`${entry.name}: ${entry.error}`)), '移动验收资源清理失败')
  return undefined
}

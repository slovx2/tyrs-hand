// Worker 标题和客户端标题摘要使用两种明确 schema，不能仅凭提示词识别。
export function isTitleOutputSchema(schema) {
  if (schema?.type !== 'object' || schema.properties?.title?.type !== 'string') return false
  const keys = Object.keys(schema.properties)
  return keys.every((key) => key === 'title' || key === 'description') &&
    (!keys.includes('description') || schema.properties.description.type === 'string')
}

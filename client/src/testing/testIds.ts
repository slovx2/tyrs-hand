export function testId(prefix: string, value: string | number): string {
  return `${prefix}:${encodeURIComponent(String(value))}`;
}

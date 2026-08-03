const offsets = new Map<string, number>();

export function loadListOffset(key: string): number {
  return offsets.get(key) ?? 0;
}

export function saveListOffset(key: string, offset: number): void {
  offsets.set(key, Math.max(0, offset));
}

export function clearListOffsets(): void {
  offsets.clear();
}


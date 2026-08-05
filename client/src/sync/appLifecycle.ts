let active = true;

export function setAppActive(value: boolean): void {
  active = value;
}

export function isAppActive(): boolean {
  return active;
}

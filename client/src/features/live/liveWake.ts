export type LiveWakeParam = string | string[] | undefined;

export function isLiveWakeParam(value: LiveWakeParam): boolean {
  return value === "1" || (Array.isArray(value) && value.includes("1"));
}

export function shouldConsumeLiveWake(value: LiveWakeParam, consumed: boolean): boolean {
  return isLiveWakeParam(value) && !consumed;
}

export function shouldAutoConnectOnOpen(wake: boolean): boolean {
  return wake;
}

export function shouldPlaySessionStartedSound(eventType: string | undefined,
  alreadyPlayed: boolean): boolean {
  return eventType === "session.started" && !alreadyPlayed;
}

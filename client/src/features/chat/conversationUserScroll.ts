export type UserScrollIntent = "none" | "away" | "bottom";

export type UserScrollGesture = {
  startOffsetY: number;
  lastOffsetY: number;
  awayThresholdCrossed: boolean;
};

export type UserScrollUpdate = {
  gesture: UserScrollGesture;
  intent: UserScrollIntent;
};

const AWAY_THRESHOLD_PX = 24;
const BOTTOM_THRESHOLD_PX = 24;

export function beginUserScroll(offsetY: number): UserScrollGesture {
  const safeOffset = finiteOffset(offsetY);
  return { startOffsetY: safeOffset, lastOffsetY: safeOffset,
    awayThresholdCrossed: false };
}

/**
 * 只根据真实手势造成的 contentOffset 位移改变跟随意图。
 * contentSize 增长只会改变 distanceFromBottom，不会被误判成用户向上阅读。
 */
export function updateUserScroll(gesture: UserScrollGesture, offsetY: number,
  distanceFromBottom: number): UserScrollUpdate {
  const safeOffset = finiteOffset(offsetY);
  const safeDistance = Math.max(0, Number.isFinite(distanceFromBottom)
    ? distanceFromBottom : 0);
  if (safeDistance <= BOTTOM_THRESHOLD_PX) {
    return { gesture: { startOffsetY: safeOffset, lastOffsetY: safeOffset,
      awayThresholdCrossed: false }, intent: "bottom" };
  }

  const movedAway = gesture.startOffsetY - safeOffset > AWAY_THRESHOLD_PX;
  const newlyAway = movedAway && !gesture.awayThresholdCrossed;
  return { gesture: { ...gesture, lastOffsetY: safeOffset,
    awayThresholdCrossed: gesture.awayThresholdCrossed || movedAway },
  intent: newlyAway ? "away" : "none" };
}

function finiteOffset(value: number): number {
  return Number.isFinite(value) ? value : 0;
}

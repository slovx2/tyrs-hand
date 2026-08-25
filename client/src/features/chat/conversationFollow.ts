import type { MobileTurn } from "@/app-server/types";

export type FollowMode = "static" | "prework_watch" | "prework_follow" | "user_follow";
export type LatestTurnPhase = "idle" | "prework" | "final_answer";
export type FollowState = { followMode: FollowMode };

export type FollowEvent =
  | { type: "latest_turn_follow_content_changed"; latestTurnPhase: LatestTurnPhase;
    followContentOverflowPx: number }
  | { type: "latest_turn_phase_changed"; previousLatestTurnPhase: LatestTurnPhase;
    latestTurnPhase: LatestTurnPhase }
  | { type: "latest_turn_placed" | "latest_turn_removed" }
  | { type: "scroll_distance_changed"; distanceFromBottomPx: number;
    latestTurnPhase: LatestTurnPhase }
  | { type: "user_reached_bottom"; latestTurnPhase: LatestTurnPhase }
  | { type: "scroll_to_bottom"; latestTurnPhase: LatestTurnPhase };

export function createFollowState(followMode: FollowMode = "static"): FollowState {
  return { followMode };
}

export function reduceFollowState(state: FollowState, event: FollowEvent): FollowState {
  switch (event.type) {
  case "latest_turn_follow_content_changed":
    return event.latestTurnPhase === "prework" && state.followMode === "prework_watch" &&
      event.followContentOverflowPx > 0 ? withMode(state, "prework_follow") : state;
  case "latest_turn_phase_changed": {
    let next = state;
    if (event.previousLatestTurnPhase !== "prework" && event.latestTurnPhase === "prework") {
      if (next.followMode === "static") next = withMode(next, "prework_watch");
      if (next.followMode === "user_follow") next = withMode(next, "prework_follow");
    }
    if (event.previousLatestTurnPhase === "prework" &&
      event.latestTurnPhase === "final_answer") {
      next = withMode(next, next.followMode === "prework_follow" ? "user_follow" : "static");
    }
    if (event.previousLatestTurnPhase !== "idle" && event.latestTurnPhase === "idle" &&
      next.followMode !== "user_follow") next = withMode(next, "static");
    return next;
  }
  case "latest_turn_placed":
  case "latest_turn_removed": return withMode(state, "static");
  case "scroll_distance_changed":
    if (event.distanceFromBottomPx <= 24) return state;
    if (state.followMode === "prework_follow") return withMode(state, "prework_watch");
    if (state.followMode === "user_follow") {
      return withMode(state, event.latestTurnPhase === "prework" ? "prework_watch" : "static");
    }
    return state;
  case "user_reached_bottom":
  case "scroll_to_bottom":
    return withMode(state, event.latestTurnPhase === "prework"
      ? "prework_follow" : "user_follow");
  }
}

export function shouldFollowLatest(state: FollowState): boolean {
  return state.followMode === "prework_follow" || state.followMode === "user_follow";
}

export function latestTurnPhase(turn: MobileTurn | null): LatestTurnPhase {
  if (!turn || turn.status !== "inProgress") return "idle";
  let hasPrework = false;
  for (const item of turn.items) {
    if (item.type === "agentMessage") {
      if (item.phase === "commentary") hasPrework = true;
      else return "final_answer";
    } else if (item.type !== "userMessage" && item.type !== "hookPrompt") {
      hasPrework = true;
    }
  }
  return hasPrework ? "prework" : "idle";
}

function withMode(state: FollowState, followMode: FollowMode): FollowState {
  return state.followMode === followMode ? state : { followMode };
}

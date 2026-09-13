import { describe, expect, it } from "vitest";
import { initialLiveTranscriptState, reduceLiveTranscript, visibleLiveTranscript } from "./transcriptReducer";

describe("Live transcript nested events", () => {
  it("reads complete text from nested content arrays as a partial", () => {
    const state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: "output_transcript.added",
      event_id: "added-1",
      item: {
        id: "item-1",
        content: [{ type: "output_text", text: "hello from content" }],
      },
    });
    expect(state.items).toEqual([]);
    expect(Object.values(state.partial)).toEqual([{ role: "assistant", text: "hello from content", eventId: "added-1" }]);
  });

  it("does not flush unrelated public deltas at turn.done", () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: "input_transcript.delta",
      item: { id: "input-1", content: [{ delta: "hello " }] },
    });
    state = reduceLiveTranscript(state, {
      type: "output_transcript.delta",
      response: { id: "response-1" },
      delta: "world",
    });
    state = reduceLiveTranscript(state, {
      type: "turn.done",
      event_id: "turn-event-1",
      turn: { id: "turn-1", role: "assistant", transcript: "world" },
    });
    expect(state.items).toEqual([{ role: "assistant", text: "world", eventId: "turn-event-1" }]);
    expect(state.partial["user:input-1"]).toEqual({ role: "user", text: "hello " });
  });

  it("grows one bubble from turn.created and turn.delta", () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: "input_transcript.added",
      event_id: "a1",
      item: { id: "item-1", text: "这是" },
    });
    state = reduceLiveTranscript(state, {
      type: "turn.created",
      event_id: "c1",
      turn: { id: "turn-1", role: "user", transcript: "这是" },
    });
    state = reduceLiveTranscript(state, {
      type: "input_transcript.added",
      event_id: "a2",
      item: { id: "item-2", text: "泰" },
    });
    state = reduceLiveTranscript(state, {
      type: "turn.delta",
      event_id: "d1",
      turn_id: "turn-1",
      delta: "泰",
    });
    expect(visibleLiveTranscript(state)).toEqual([{ role: "user", text: "这是泰", eventId: "d1" }]);
    state = reduceLiveTranscript(state, {
      type: "turn.done",
      event_id: "done-1",
      turn: { id: "turn-1", role: "user", transcript: "这是泰" },
    });
    expect(state.items).toEqual([{ role: "user", text: "这是泰", eventId: "done-1" }]);
    expect(state.partial).toEqual({});
  });
});

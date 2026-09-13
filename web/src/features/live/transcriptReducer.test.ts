import { describe, expect, it } from 'vitest'
import {
  initialLiveTranscriptState,
  reduceLiveTranscript,
  visibleLiveTranscript,
} from './transcriptReducer'

describe('Live transcript reducer', () => {
  it('accumulates and commits input and output deltas', () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: 'session.input_transcript.delta',
      item_id: 'i',
      delta: 'hel',
    })
    state = reduceLiveTranscript(state, {
      type: 'session.input_transcript.delta',
      item_id: 'i',
      delta: 'lo',
    })
    state = reduceLiveTranscript(state, {
      type: 'session.input_transcript.done',
      item_id: 'i',
    })
    state = reduceLiveTranscript(state, {
      type: 'session.output_transcript.done',
      response_id: 'r',
      text: 'world',
    })
    expect(state.items).toEqual([
      { role: 'user', text: 'hello' },
      { role: 'assistant', text: 'world' },
    ])
  })
  it('keeps added transcripts as partials until turn or done', () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: 'input_transcript.added',
      eventId: 'input-1',
      itemId: 'item-1',
      transcript: 'hello',
    })
    state = reduceLiveTranscript(state, {
      type: 'input_transcript.added',
      eventId: 'input-1',
      itemId: 'item-1',
      transcript: 'hello',
    })
    expect(state.items).toEqual([])
    expect(Object.values(state.partial)).toEqual([
      { role: 'user', text: 'hello', eventId: 'input-1' },
    ])
    state = reduceLiveTranscript(state, {
      type: 'output_transcript.delta',
      event_id: 'output-1',
      responseId: 'response-1',
      delta: 'world',
    })
    state = reduceLiveTranscript(state, {
      type: 'output_transcript.completed',
      event_id: 'output-2',
      responseId: 'response-1',
    })
    expect(state.items).toEqual([
      { role: 'assistant', text: 'world', eventId: 'output-2' },
    ])
    expect(Object.values(state.partial)).toEqual([
      { role: 'user', text: 'hello', eventId: 'input-1' },
    ])
  })
  it('merges unique added fragments into one bubble and commits on turn.done', () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: 'input_transcript.added',
      event_id: 'a1',
      item: { id: 'item-1', text: '这是' },
    })
    state = reduceLiveTranscript(state, {
      type: 'input_transcript.added',
      event_id: 'a2',
      item: { id: 'item-2', text: '泰' },
    })
    expect(visibleLiveTranscript(state)).toEqual([
      { role: 'user', text: '这是泰', eventId: 'a2' },
    ])
    state = reduceLiveTranscript(state, {
      type: 'turn.done',
      event_id: 'done-1',
      turn: {
        id: 'turn-1',
        role: 'user',
        transcript: '这是泰尔斯·汉德实时语音验收,请重复这句话',
      },
    })
    expect(state.items).toEqual([
      {
        role: 'user',
        text: '这是泰尔斯·汉德实时语音验收,请重复这句话',
        eventId: 'done-1',
      },
    ])
    expect(state.partial).toEqual({})
  })
})

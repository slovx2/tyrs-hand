import { describe, expect, it } from 'vitest'
import {
  initialLiveTranscriptState,
  reduceLiveTranscript,
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
  it('supports complete events and deduplicates event ids', () => {
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
      { role: 'user', text: 'hello', eventId: 'input-1' },
      { role: 'assistant', text: 'world', eventId: 'output-2' },
    ])
  })
})

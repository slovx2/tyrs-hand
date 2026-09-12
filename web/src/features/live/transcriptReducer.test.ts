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
})

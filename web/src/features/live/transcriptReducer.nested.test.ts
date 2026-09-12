import { describe, expect, it } from 'vitest'
import {
  initialLiveTranscriptState,
  reduceLiveTranscript,
} from './transcriptReducer'

describe('Live transcript nested events', () => {
  it('reads complete text from nested content arrays', () => {
    const state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: 'output_transcript.added',
      event_id: 'added-1',
      item: {
        id: 'item-1',
        content: [{ type: 'output_text', text: 'hello from content' }],
      },
    })
    expect(state.items).toEqual([
      { role: 'assistant', text: 'hello from content', eventId: 'added-1' },
    ])
  })

  it('flushes all pending deltas at turn.done without duplicating later done events', () => {
    let state = reduceLiveTranscript(initialLiveTranscriptState, {
      type: 'input_transcript.delta',
      item: { id: 'input-1', content: [{ delta: 'hello ' }] },
    })
    state = reduceLiveTranscript(state, {
      type: 'output_transcript.delta',
      response: { id: 'response-1', content: [{ delta: 'world' }] },
    })
    state = reduceLiveTranscript(state, { type: 'turn.done', id: 'turn-1' })
    state = reduceLiveTranscript(state, {
      type: 'output_transcript.done',
      response_id: 'response-1',
      id: 'done-1',
    })
    expect(state.items).toEqual([
      { role: 'user', text: 'hello ' },
      { role: 'assistant', text: 'world' },
    ])
  })
})

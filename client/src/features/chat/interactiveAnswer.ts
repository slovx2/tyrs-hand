export function buildInteractiveAnswer(answers: Record<string, string>) {
  return {
    answers: Object.fromEntries(Object.entries(answers).map(([id, answer]) => [
      id, { answers: [answer] },
    ])),
  };
}

export type ComposerPrimaryAction = {
  kind: "send" | "stop";
  disabled: boolean;
};

export function composerPrimaryAction(active: boolean, hasContent: boolean,
  busy: boolean): ComposerPrimaryAction {
  const kind = active && !hasContent ? "stop" : "send";
  return { kind, disabled: busy || kind === "send" && !hasContent };
}

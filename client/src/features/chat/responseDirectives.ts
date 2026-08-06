const nonRenderedGitDirectives = new Set([
  "git-stage",
  "git-commit",
  "git-create-branch",
  "git-push",
  "git-create-pr",
]);

type Fence = { marker: "`" | "~"; length: number };

export type ResponseDirective = {
  name: string;
  attributes: Record<string, string | true>;
};

function fenceAt(line: string): Fence | null {
  const leadingSpaces = line.length - line.trimStart().length;
  if (leadingSpaces > 3) return null;
  const value = line.slice(leadingSpaces);
  const marker = value[0];
  if (marker !== "`" && marker !== "~") return null;
  let length = 0;
  while (value[length] === marker) length++;
  return length >= 3 ? { marker, length } : null;
}

function closesFence(line: string, fence: Fence): boolean {
  const candidate = fenceAt(line);
  if (!candidate || candidate.marker !== fence.marker || candidate.length < fence.length) return false;
  const leadingSpaces = line.length - line.trimStart().length;
  return line.slice(leadingSpaces + candidate.length).trim() === "";
}

function isNameStart(character: string | undefined): boolean {
  return character !== undefined && /[a-zA-Z]/.test(character);
}

function isNameCharacter(character: string | undefined): boolean {
  return character !== undefined && /[a-zA-Z0-9_-]/.test(character);
}

function parseQuotedValue(value: string, start: number): { value: string; next: number } | null {
  let parsed = "";
  let escaped = false;
  for (let cursor = start + 1; cursor < value.length; cursor++) {
    const character = value[cursor];
    if (escaped) {
      parsed += character;
      escaped = false;
    } else if (character === "\\") {
      escaped = true;
    } else if (character === '"') {
      return { value: parsed, next: cursor + 1 };
    } else {
      parsed += character;
    }
  }
  return null;
}

export function parseResponseDirective(line: string): ResponseDirective | null {
  const leadingSpaces = line.length - line.trimStart().length;
  if (leadingSpaces > 3) return null;
  const value = line.trim();
  if (!value.startsWith("::") || !isNameStart(value[2])) return null;
  let cursor = 3;
  while (isNameCharacter(value[cursor])) cursor++;
  const name = value.slice(2, cursor);
  if (value[cursor] !== "{") return null;
  cursor++;
  const attributes: Record<string, string | true> = {};
  while (cursor < value.length) {
    while (/\s/.test(value[cursor] ?? "")) cursor++;
    if (value[cursor] === "}") {
      cursor++;
      return cursor === value.length ? { name, attributes } : null;
    }
    if (!isNameStart(value[cursor])) return null;
    const keyStart = cursor++;
    while (isNameCharacter(value[cursor])) cursor++;
    const key = value.slice(keyStart, cursor);
    if (value[cursor] !== "=") return null;
    cursor++;
    if (value[cursor] === '"') {
      const quoted = parseQuotedValue(value, cursor);
      if (!quoted) return null;
      attributes[key] = quoted.value;
      cursor = quoted.next;
    } else {
      const valueStart = cursor;
      while (cursor < value.length && !/\s|}/.test(value.charAt(cursor))) cursor++;
      if (valueStart === cursor) return null;
      const attributeValue = value.slice(valueStart, cursor);
      attributes[key] = attributeValue === "true" ? true : attributeValue;
    }
  }
  return null;
}

export function renderableFinalAnswer(value: string): string {
  let fence: Fence | null = null;
  const visible = value.split("\n").filter((line) => {
    if (fence) {
      if (closesFence(line, fence)) fence = null;
      return true;
    }
    const openingFence = fenceAt(line);
    if (openingFence) {
      fence = openingFence;
      return true;
    }
    const directive = parseResponseDirective(line);
    return directive === null || !nonRenderedGitDirectives.has(directive.name);
  });
  return visible.join("\n").trimEnd();
}

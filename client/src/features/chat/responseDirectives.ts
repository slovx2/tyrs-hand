const nonRenderedGitDirectives = new Set([
  "git-stage",
  "git-commit",
  "git-create-branch",
  "git-push",
  "git-create-pr",
]);

const hiddenDirectiveNames = new Set([
  ...nonRenderedGitDirectives,
  "archive-thread",
  "code-comment",
  "codex-realtime-inline",
  "created-thread",
  "inbox-item",
  "pr-auto-fix-progress",
  "thread-purpose-changed",
]);

const placeholderPrefix = "\uE000codex-";
const placeholderRegistry = new Map<string, MarkdownPlaceholder>();
let placeholderSequence = 0;

type Fence = { marker: "`" | "~"; length: number };

export type ResponseDirective = {
  name: string;
  attributes: Record<string, string | true>;
};

export type MarkdownPlaceholder =
  | { kind: "file-citation"; path: string; lineStart: number; lineEnd?: number }
  | { kind: "task-stub"; title: string; prompt: string }
  | { kind: "writing"; variant: string; content: string }
  | { kind: "artifact-template"; displayName: string; artifactKind: string }
  | { kind: "html-inline"; variant: "u" | "sub" | "sup"; content: string };

export type PreparedMarkdown = { source: string };

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

function parseDirectiveAt(value: string, markerLength: 2 | 3): ResponseDirective | null {
  const leadingSpaces = value.length - value.trimStart().length;
  if (leadingSpaces > 3) return null;
  const trimmed = value.trim();
  if (!trimmed.startsWith(":".repeat(markerLength)) || !isNameStart(trimmed[markerLength])) {
    return null;
  }
  let cursor = markerLength + 1;
  while (isNameCharacter(trimmed[cursor])) cursor++;
  const name = trimmed.slice(markerLength, cursor);
  if (trimmed[cursor] !== "{") return null;
  cursor++;
  const attributes: Record<string, string | true> = {};
  while (cursor < trimmed.length) {
    while (/\s/.test(trimmed[cursor] ?? "")) cursor++;
    if (trimmed[cursor] === "}") {
      cursor++;
      return cursor === trimmed.length ? { name, attributes } : null;
    }
    if (!isNameStart(trimmed[cursor])) return null;
    const keyStart = cursor++;
    while (isNameCharacter(trimmed[cursor])) cursor++;
    const key = trimmed.slice(keyStart, cursor);
    if (trimmed[cursor] !== "=") return null;
    cursor++;
    if (trimmed[cursor] === '"') {
      const quoted = parseQuotedValue(trimmed, cursor);
      if (!quoted) return null;
      attributes[key] = quoted.value;
      cursor = quoted.next;
    } else {
      const valueStart = cursor;
      while (cursor < trimmed.length && !/\s|}/.test(trimmed.charAt(cursor))) cursor++;
      if (valueStart === cursor) return null;
      const attributeValue = trimmed.slice(valueStart, cursor);
      attributes[key] = attributeValue === "true" ? true : attributeValue;
    }
  }
  return null;
}

export function parseContainerDirective(line: string): ResponseDirective | null {
  return parseDirectiveAt(line, 3);
}

export function lookupMarkdownPlaceholder(value: string): MarkdownPlaceholder | null {
  return placeholderRegistry.get(value) ?? null;
}

function registerPlaceholder(value: MarkdownPlaceholder, hint: string): string {
  const token = `${placeholderPrefix}${hint}-${placeholderSequence++}\uE001`;
  placeholderRegistry.set(token, value);
  while (placeholderRegistry.size > 512) {
    const oldest = placeholderRegistry.keys().next().value as string | undefined;
    if (oldest === undefined) break;
    placeholderRegistry.delete(oldest);
  }
  return token;
}

function fileCitationPlaceholder(path: string, lineStart: number, lineEnd?: number): string {
  return registerPlaceholder({ kind: "file-citation", path, lineStart,
    ...(lineEnd === undefined ? {} : { lineEnd }) }, "file");
}

function normalizeBasicHtml(line: string): string {
  return transformOutsideCodeSpans(line, (segment) => normalizeBasicHtmlSegment(segment));
}

function normalizeBasicHtmlSegment(line: string): string {
  let value = line.replace(/<br\s*\/?>/gi, "\n").replace(/<hr\s*\/?>/gi, "\n---\n");
  value = value.replace(/<\/?(?:strong|b)>/gi, "**")
    .replace(/<\/?(?:em|i)>/gi, "*")
    .replace(/<\/?(?:del|s)>/gi, "~~")
    .replace(/<\/?kbd>/gi, "`");
  value = value.replace(/<(u|sub|sup)>([^<>]*)<\/\1>/gi, (_match, tag: string, content: string) =>
    registerPlaceholder({ kind: "html-inline", variant: tag.toLowerCase() as "u" | "sub" | "sup", content }, "html"));
  return value;
}

function replaceFileCitations(line: string): string {
  return transformOutsideCodeSpans(line, (segment) => segment.replace(/【([^†\n】]+)†L(\d+)(?:-L?(\d+))?】/g,
    (_match, rawPath: string, rawStart: string, rawEnd?: string) => {
      const path = rawPath.startsWith("F:") ? rawPath.slice(2).trim() : rawPath.trim();
      return fileCitationPlaceholder(path, Number(rawStart), rawEnd ? Number(rawEnd) : undefined);
    }));
}

function replaceFileCitationDirectives(line: string): string {
  return transformOutsideCodeSpans(line, (segment) => segment.replace(/:{1,2}codex-file-citation\{[^\n}]*\}/g, (raw) => {
    const directive = parseResponseDirective(raw.startsWith("::") ? raw : `:${raw}`);
    const path = directive?.attributes.path;
    const rawStart = directive?.attributes.line_range_start ?? directive?.attributes.lineRangeStart;
    const rawEnd = directive?.attributes.line_range_end ?? directive?.attributes.lineRangeEnd;
    const lineStart = typeof rawStart === "string" ? Number(rawStart) : Number.NaN;
    const lineEnd = typeof rawEnd === "string" ? Number(rawEnd) : undefined;
    if (typeof path !== "string" || !Number.isInteger(lineStart) || lineStart <= 0 ||
      lineEnd !== undefined && (!Number.isInteger(lineEnd) || lineEnd <= 0)) return raw;
    return fileCitationPlaceholder(path, lineStart, lineEnd);
  }));
}

function transformOutsideCodeSpans(value: string, transform: (segment: string) => string): string {
  const codeSpan = /`+[^`]*`+/g;
  let output = "";
  let cursor = 0;
  for (const match of value.matchAll(codeSpan)) {
    const start = match.index ?? 0;
    output += transform(value.slice(cursor, start)) + match[0];
    cursor = start + match[0].length;
  }
  return output + transform(value.slice(cursor));
}

function taskListMarker(line: string): string {
  return line.replace(/^(\s*[-*+]\s+)\[([ xX])\]\s+/, (_match, prefix: string, checked: string) =>
    `${prefix}${checked.toLowerCase() === "x" ? "☑" : "☐"} `);
}

export function prepareMarkdown(value: string): PreparedMarkdown {
  const lines = value.replace(/\r\n?/g, "\n").split("\n");
  const output: string[] = [];
  let fence: Fence | null = null;
  for (let index = 0; index < lines.length; index++) {
    const original = lines[index] ?? "";
    if (fence) {
      output.push(original);
      if (closesFence(original, fence)) fence = null;
      continue;
    }
    const openingFence = fenceAt(original);
    if (openingFence) {
      fence = openingFence;
      output.push(original);
      continue;
    }
    const trimmed = original.trim();
    if (trimmed.startsWith(":::") && trimmed !== ":::") {
      const directive = parseContainerDirective(original);
      if (directive) {
        const body: string[] = [];
        let closeIndex = index + 1;
        for (; closeIndex < lines.length; closeIndex++) {
          if ((lines[closeIndex] ?? "").trim() === ":::") break;
          body.push(lines[closeIndex] ?? "");
        }
        if (directive.name === "writing") {
          const token = registerPlaceholder({ kind: "writing",
            variant: String(directive.attributes.variant ?? "standard"), content: body.join("\n") }, "block");
          output.push("", token, "");
        } else if (directive.name === "task-stub") {
          const token = registerPlaceholder({ kind: "task-stub",
            title: String(directive.attributes.title ?? "建议任务"), prompt: body.join("\n") }, "block");
          output.push("", token, "");
        } else if (directive.name === "github-details") {
          output.push(...body);
        } else if (!hiddenDirectiveNames.has(directive.name)) {
          output.push(...body);
        }
        index = closeIndex < lines.length ? closeIndex : lines.length;
        continue;
      }
    }
    const directive = parseResponseDirective(original);
    if (directive) {
      if (directive.name === "task-stub") {
        output.push("", registerPlaceholder({ kind: "task-stub",
          title: String(directive.attributes.title ?? "建议任务"), prompt: String(directive.attributes.prompt ?? "") }, "block"), "");
        continue;
      }
      if (directive.name === "artifact-template") {
        output.push("", registerPlaceholder({ kind: "artifact-template",
          displayName: String(directive.attributes.display_name ?? directive.attributes.displayName ?? "产物模板"),
          artifactKind: String(directive.attributes.artifact_kind ?? directive.attributes.artifactKind ?? "artifact") }, "block"), "");
        continue;
      }
      if (hiddenDirectiveNames.has(directive.name) || directive.name === "automation-citation" ||
        directive.name === "codex-inline-vis" || directive.name === "codex-live-vis") continue;
    }
    let line = original.replace(/[^]*/g, "")
      .replace(/:{1,2}chatgpt-(?:content-reference|citation|dil|image-group)\{[^\n}]*\}/g, "");
    line = line.replace(/:codex-annotation\{[^\n}]*\}/g, "");
    line = normalizeBasicHtml(line);
    line = replaceFileCitationDirectives(line);
    line = replaceFileCitations(line);
    output.push(taskListMarker(line));
  }
  return { source: output.join("\n") };
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
    return directive === null || !hiddenDirectiveNames.has(directive.name);
  });
  return visible.join("\n").trimEnd();
}

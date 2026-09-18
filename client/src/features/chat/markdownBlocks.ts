import type { ASTNode } from "react-native-markdown-display";

export type MarkdownBlock = { key: string; ast: ASTNode[]; sizeHint?: number; resolveAst?: () => ASTNode[] };
export type MarkdownSplitter = (source: string, key: string) => MarkdownBlock[];
const TEXT_CHUNK_SIZE = 2400;

/** 顶格 ATX 标题是确定的块边界；无跨块引用时可避免对长章节文档预解析整篇。 */
export function headingSourceChunks(source: string): string[] | null {
  if (!source.includes("\n#") || source.includes("]:")) return null;
  const chunks: string[] = [];
  let start = 0, offset = 0;
  let fence: { marker: string; length: number } | null = null;
  for (const line of source.split("\n")) {
    const first = line[0];
    if (first === "`" || first === "~" || first === " ") {
      const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
      if (match) {
        const marker = match[1]!;
        if (!fence && (marker[0] !== "`" || !match[2]!.includes("`"))) {
          fence = { marker: marker[0]!, length: marker.length };
        } else if (fence && fence.marker === marker[0] && marker.length >= fence.length && !match[2]!.trim()) {
          fence = null;
        }
      }
    }
    if (!fence && first === "#" && /^#{1,6}(?:[ \t]|$)/.test(line) && offset - start >= 800) {
      chunks.push(source.slice(start, offset)); start = offset;
    }
    offset += line.length + 1;
  }
  if (!chunks.length) return null;
  chunks.push(source.slice(start));
  return chunks;
}

/** 仅在解析器确认的顶层块边界分组，围栏、表格、列表及跨行内联格式保持完整。 */
export function markdownSourceChunks(source: string,
  tokens: { nesting: number; map: [number, number] | null }[]): string[] {
  const offsets = [0];
  for (let index = 0; index < source.length; index += 1) {
    if (source[index] === "\n") offsets.push(index + 1);
  }
  let depth = 0;
  let start = 0;
  const chunks: string[] = [];
  for (const token of tokens) {
    if (depth === 0 && token.map && token.nesting >= 0) {
      const boundary = offsets[token.map[0]] ?? source.length;
      if (boundary - start >= 800) {
        chunks.push(source.slice(start, boundary)); start = boundary;
      }
    }
    depth += token.nesting;
  }
  if (start < source.length) chunks.push(source.slice(start));
  return chunks;
}

/** 先解析完整文档，再切 AST；引用链接、围栏、嵌套格式不会被源码切片破坏。 */
export function splitMarkdownAst(ast: ASTNode[]): MarkdownBlock[] {
  return ast.flatMap(splitNode).map((node, index) => ({ key: String(index), ast: [node] }));
}

function splitNode(node: ASTNode): ASTNode[] {
  if (node.type === "body" || node.type === "blockquote") {
    return node.children.flatMap(splitNode).map((child) => ({ ...node, children: [child] }));
  }
  if (node.type === "bullet_list" || node.type === "ordered_list") {
    return node.children.map((child) => ({ ...node, children: [child] }));
  }
  if (node.type === "table") {
    const columns = Math.max(0, ...node.children.flatMap((section) =>
      section.children.map((row) => row.children.length)));
    return node.children.flatMap((section) => section.children.map((row) => ({ ...node,
      attributes: { ...node.attributes, virtualColumnCount: columns },
      children: [{ ...section, children: [row] }] })));
  }
  if (node.type === "fence" || node.type === "code_block") {
    const chunks: string[] = [];
    let current = "";
    let lines = 0;
    for (const line of node.content.match(/[^\n]*\n|[^\n]+$/g) ?? []) {
      if (current && (lines >= 40 || current.length + line.length > TEXT_CHUNK_SIZE)) {
        chunks.push(current); current = ""; lines = 0;
      }
      const pieces = splitText(line);
      chunks.push(...pieces.slice(0, -1));
      current += pieces.at(-1) ?? "";
      lines += 1;
    }
    if (current) chunks.push(current);
    return chunks.map((content, index) => ({ ...node, key: `${node.key}:${index}`, content }));
  }
  if (node.type === "paragraph") {
    return splitInline(node);
  }
  return [node];
}

function splitText(text: string): string[] {
  const result: string[] = [];
  for (let start = 0; start < text.length;) {
    let end = Math.min(start + TEXT_CHUNK_SIZE, text.length);
    const last = text.charCodeAt(end - 1);
    if (end < text.length && last >= 0xd800 && last <= 0xdbff) end -= 1;
    result.push(text.slice(start, end));
    start = end;
  }
  return result;
}

function splitInline(node: ASTNode): ASTNode[] {
  if (node.children.length === 0) {
    return (node.content?.length ?? 0) > TEXT_CHUNK_SIZE
      ? splitText(node.content).map((content, index) => ({ ...node,
        key: `${node.key}:${index}`, content })) : [node];
  }
  const result: ASTNode[] = [];
  let children: ASTNode[] = [];
  let length = 0;
  for (const child of node.children.flatMap(splitInline)) {
    const size = inlineLength(child);
    if (children.length > 0 && length + size > TEXT_CHUNK_SIZE) {
      result.push({ ...node, children }); children = []; length = 0;
    }
    children.push(child); length += size;
  }
  if (children.length > 0) result.push({ ...node, children });
  return result;
}

function inlineLength(node: ASTNode): number {
  // Markdown 库生成的 textgroup Token 没有 content（其类型声明却要求 string）。
  return (node.content?.length ?? 0) + node.children.reduce((sum, child) => sum + inlineLength(child), 0);
}

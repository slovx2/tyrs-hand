import { Fragment, memo, type ReactNode, useEffect, useMemo, useState } from "react";
import { Platform, StyleSheet, Text, View } from "react-native";
import Markdown, { type ASTNode, MarkdownIt, parser, type RenderFunction,
  type RenderRules } from "react-native-markdown-display";

import { useTheme } from "@/theme/ThemeProvider";
import { useRenderScheduler } from "@/render/renderScheduler";
import { CachedMessageImage } from "@/features/images/CachedMessageImage";
import { RemoteMessageImage } from "@/features/images/RemoteMessageImage";
import { lookupMarkdownPlaceholder, prepareMarkdown, type MarkdownPlaceholder } from "./responseDirectives";

type MarkdownContentProps = {
  children: string;
  cacheKey: string;
  profileId: string;
  compact?: boolean;
  defer?: boolean;
  imageTestPrefix?: string;
  onFileCitationPress?: (path: string, lineStart: number, lineEnd?: number) => void;
};

const monoFont = Platform.select({ ios: "Menlo", android: "monospace", default: "monospace" });
const markdownIt = new MarkdownIt({ breaks: true, typographer: true });
const MAX_AST_CACHE_ENTRIES = 48;
const MAX_AST_CACHE_SOURCE_CHARACTERS = 160_000;
const MAX_AST_ENTRY_SOURCE_CHARACTERS = 32_000;

type MarkdownAstCacheEntry = {
  source: string;
  ast: ASTNode[];
};

const markdownAstCache = new Map<string, MarkdownAstCacheEntry>();
let markdownAstCacheSourceCharacters = 0;

const identityRenderer = ((nodes: ASTNode[]) => nodes) as unknown as Parameters<typeof parser>[1];

function parseMarkdownAst(source: string): ASTNode[] {
  return parser(source, identityRenderer, markdownIt) as ASTNode[];
}

function cachedMarkdownAst(cacheKey: string, source: string): ASTNode[] {
  const cached = markdownAstCache.get(cacheKey);
  if (cached?.source === source) {
    markdownAstCache.delete(cacheKey);
    markdownAstCache.set(cacheKey, cached);
    return cached.ast;
  }
  if (cached) {
    markdownAstCache.delete(cacheKey);
    markdownAstCacheSourceCharacters -= cached.source.length;
  }

  const ast = parseMarkdownAst(source);
  if (source.length > MAX_AST_ENTRY_SOURCE_CHARACTERS) return ast;

  markdownAstCache.set(cacheKey, { source, ast });
  markdownAstCacheSourceCharacters += source.length;
  while (markdownAstCache.size > MAX_AST_CACHE_ENTRIES ||
    markdownAstCacheSourceCharacters > MAX_AST_CACHE_SOURCE_CHARACTERS) {
    const oldestKey = markdownAstCache.keys().next().value as string | undefined;
    if (oldestKey === undefined) break;
    const oldest = markdownAstCache.get(oldestKey);
    markdownAstCache.delete(oldestKey);
    markdownAstCacheSourceCharacters -= oldest?.source.length ?? 0;
  }
  return ast;
}

const textBlockStyle: Record<string, string> = {
  paragraph: "paragraphBlockText",
  heading1: "heading1BlockText",
  heading2: "heading2BlockText",
  heading3: "heading3BlockText",
  heading4: "heading4BlockText",
  heading5: "heading5BlockText",
  heading6: "heading6BlockText",
};

const selectableTextGroup: RenderFunction = (node, children, parents, styles) => {
  const parent = parents[0];
  const blockStyle = parent && !containsVisualNode(parent)
    ? styles[textBlockStyle[parent.type] ?? ""] : undefined;
  return <Text key={node.key} selectable style={[styles.textgroup, blockStyle]}>{children}</Text>;
};

const lightweightTextBlock: RenderFunction = (node, children, _parents, styles) =>
  containsVisualNode(node)
    ? <View key={node.key} style={styles[`_VIEW_SAFE_${node.type}`]}>{children}</View>
    : <Fragment key={node.key}>{children}</Fragment>;

const selectableCode: RenderFunction = (node, _children, _parents, styles, inheritedStyles = {}) => {
  const content = node.content.endsWith("\n") ? node.content.slice(0, -1) : node.content;
  return <Text key={node.key} selectable style={[inheritedStyles, styles[node.type]]}>{content}</Text>;
};

const selectableRules: RenderRules = {
  textgroup: selectableTextGroup,
  paragraph: lightweightTextBlock,
  heading1: lightweightTextBlock,
  heading2: lightweightTextBlock,
  heading3: lightweightTextBlock,
  heading4: lightweightTextBlock,
  heading5: lightweightTextBlock,
  heading6: lightweightTextBlock,
  code_block: selectableCode,
  fence: selectableCode,
};

function containsVisualNode(node: ASTNode): boolean {
  return node.type === "image" || node.type === "blocklink" ||
    node.children.some(containsVisualNode);
}

function soleText(node: ASTNode): string | null {
  if (node.type === "text") return node.content;
  if (node.children.length !== 1) return null;
  return soleText(node.children[0]!);
}

const placeholderPattern = /\uE000codex-[^\uE001]+\uE001/g;

function fileLabel(path: string, lineStart: number, lineEnd?: number): string {
  const name = path.split(/[\\/]/).at(-1) || path;
  const location = lineEnd === undefined ? `L${lineStart}` : `L${lineStart}-L${lineEnd}`;
  return `${name} · ${location}`;
}

function MarkdownDirectiveCard({ directive, styles }: {
  directive: Extract<MarkdownPlaceholder, { kind: "task-stub" | "writing" | "artifact-template" }>;
  styles: Record<string, any>;
}) {
  if (directive.kind === "task-stub") {
    return <View style={styles.codexCard}>
      <Text style={styles.codexCardLabel}>建议任务</Text>
      <Text style={styles.codexCardTitle}>{directive.title}</Text>
      {directive.prompt ? <Text selectable style={styles.codexCardBody}>{directive.prompt}</Text> : null}
    </View>;
  }
  if (directive.kind === "artifact-template") {
    return <View style={styles.codexCard}>
      <Text style={styles.codexCardLabel}>产物模板 · {directive.artifactKind}</Text>
      <Text style={styles.codexCardTitle}>{directive.displayName}</Text>
    </View>;
  }
  return <View style={styles.codexCard}>
    <Text style={styles.codexCardLabel}>写作内容 · {directive.variant}</Text>
    <Text selectable style={styles.codexCardBody}>{directive.content}</Text>
  </View>;
}

function renderMarkdownText(node: ASTNode, _children: ReactNode[], _parents: ASTNode[], styles: any,
  inheritedStyles: Record<string, any> = {}, onFileCitationPress?: MarkdownContentProps["onFileCitationPress"]) {
  const parts = node.content.split(placeholderPattern);
  const matches = node.content.match(placeholderPattern) ?? [];
  if (matches.length === 0) {
    return <Text key={node.key} style={[inheritedStyles, styles.text]}>{node.content}</Text>;
  }
  const children: ReactNode[] = [];
  let matchIndex = 0;
  parts.forEach((part, index) => {
    if (part) children.push(<Text key={`${node.key}:text:${index}`}>{part}</Text>);
    const token = matches[matchIndex++];
    if (!token) return;
    const directive = lookupMarkdownPlaceholder(token);
    if (directive?.kind === "file-citation") {
      children.push(<Text key={`${node.key}:citation:${index}`} accessibilityRole="link"
        onPress={onFileCitationPress ? () => onFileCitationPress(directive.path, directive.lineStart,
          directive.lineEnd) : undefined} style={styles.fileCitation}>
        {fileLabel(directive.path, directive.lineStart, directive.lineEnd)}
      </Text>);
    } else if (directive?.kind === "html-inline" &&
      (directive.variant === "u" || directive.variant === "sub" || directive.variant === "sup")) {
      const style = directive.variant === "u" ? styles.htmlUnderline :
        directive.variant === "sub" ? styles.htmlSub : styles.htmlSup;
      children.push(<Text key={`${node.key}:html:${index}`} style={style}>{directive.content}</Text>);
    }
  });
  return <Text key={node.key} style={[inheritedStyles, styles.text]}>{children}</Text>;
}

export const MarkdownContent = memo(function MarkdownContent({ children, cacheKey, profileId,
  compact = false, defer = false, imageTestPrefix = "markdown:image", onFileCitationPress }: MarkdownContentProps) {
  const theme = useTheme();
  const scheduler = useRenderScheduler();
  const prepared = useMemo(() => prepareMarkdown(children), [children]);
  const [ast, setAst] = useState<ASTNode[] | null>(() => defer ? null :
    cachedMarkdownAst(cacheKey, prepared.source));
  useEffect(() => {
    if (!defer) {
      setAst(cachedMarkdownAst(cacheKey, prepared.source));
      return;
    }
    let cancelled = false;
    setAst(null);
    const cancel = scheduler.afterInteractions(() => {
      scheduler.schedule(() => {
        if (cancelled) return;
        setAst(cachedMarkdownAst(cacheKey, prepared.source));
      }, "background");
    });
    return () => {
      cancelled = true;
      cancel();
    };
  }, [cacheKey, defer, prepared.source, scheduler]);
  const blockGap = compact ? 5 : 8;
  const markdownStyle = useMemo(() => StyleSheet.create({
    body: { width: "100%" },
    text: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    textgroup: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    paragraph: { width: "100%", flexDirection: "row", flexWrap: "wrap", alignItems: "flex-start",
      marginTop: 0, marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_400Regular",
      fontSize: 15, lineHeight: 24 },
    paragraphBlockText: { width: "100%", marginTop: 0, marginBottom: blockGap },
    heading1: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 8 : 12,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 24,
      lineHeight: 32 },
    heading1BlockText: { width: "100%", marginTop: compact ? 8 : 12,
      marginBottom: blockGap },
    heading2: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 7 : 10,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 21,
      lineHeight: 29 },
    heading2BlockText: { width: "100%", marginTop: compact ? 7 : 10,
      marginBottom: blockGap },
    heading3: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 6 : 8,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 18,
      lineHeight: 26 },
    heading3BlockText: { width: "100%", marginTop: compact ? 6 : 8,
      marginBottom: blockGap },
    heading4: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 6,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 16,
      lineHeight: 24 },
    heading4BlockText: { width: "100%", marginTop: 6, marginBottom: blockGap },
    heading5: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 4,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 15,
      lineHeight: 23 },
    heading5BlockText: { width: "100%", marginTop: 4, marginBottom: blockGap },
    heading6: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 4,
      marginBottom: blockGap, color: theme.colors.textMuted, fontFamily: "Inter_600SemiBold", fontSize: 14,
      lineHeight: 22 },
    heading6BlockText: { width: "100%", marginTop: 4, marginBottom: blockGap },
    strong: { color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontWeight: "600" },
    em: { color: theme.colors.text, fontStyle: "italic" },
    s: { color: theme.colors.textMuted, textDecorationLine: "line-through" },
    link: { color: theme.colors.accent, textDecorationLine: "underline" },
    blocklink: { borderBottomWidth: 0 },
    blockquote: { width: "100%", marginTop: 1, marginBottom: blockGap, paddingVertical: 4,
      paddingLeft: 12, paddingRight: 8, borderLeftWidth: 3, borderLeftColor: theme.colors.border,
      backgroundColor: theme.colors.surfaceAlt },
    bullet_list: { width: "100%", marginTop: 0, marginBottom: blockGap },
    ordered_list: { width: "100%", marginTop: 0, marginBottom: blockGap },
    list_item: { width: "100%", flexDirection: "row", alignItems: "flex-start", color: theme.colors.text,
      fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    bullet_list_icon: { width: 18, marginLeft: 2, marginRight: 4, color: theme.colors.textMuted,
      fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    bullet_list_content: { flex: 1, minWidth: 0 },
    ordered_list_icon: { minWidth: 22, marginLeft: 0, marginRight: 4, color: theme.colors.textMuted,
      fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24, textAlign: "right" },
    ordered_list_content: { flex: 1, minWidth: 0 },
    code_inline: { color: theme.colors.text, backgroundColor: theme.colors.surfaceAlt, fontFamily: monoFont,
      fontSize: 14, lineHeight: 21, borderWidth: 0, borderRadius: 4, paddingHorizontal: 4, paddingVertical: 1 },
    code_block: { width: "100%", color: theme.colors.text, backgroundColor: theme.colors.surfaceAlt,
      borderColor: theme.colors.border, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
      paddingHorizontal: 12, paddingVertical: 10, marginTop: 1, marginBottom: blockGap, fontFamily: monoFont,
      fontSize: 13, lineHeight: 20 },
    fence: { width: "100%", color: theme.colors.text, backgroundColor: theme.colors.surfaceAlt,
      borderColor: theme.colors.border, borderWidth: StyleSheet.hairlineWidth, borderRadius: 8,
      paddingHorizontal: 12, paddingVertical: 10, marginTop: 1, marginBottom: blockGap, fontFamily: monoFont,
      fontSize: 13, lineHeight: 20 },
    hr: { width: "100%", height: StyleSheet.hairlineWidth, marginVertical: compact ? 7 : 10,
      backgroundColor: theme.colors.border },
    table: { width: "100%", marginTop: 1, marginBottom: blockGap, borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border, borderRadius: 6, overflow: "hidden" },
    thead: { backgroundColor: theme.colors.surfaceAlt },
    tbody: {},
    th: { flex: 1, minWidth: 0, paddingHorizontal: 8, paddingVertical: 7 },
    tr: { width: "100%", flexDirection: "row", borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border },
    td: { flex: 1, minWidth: 0, paddingHorizontal: 8, paddingVertical: 7 },
    image: { width: "100%" },
    hardbreak: { width: "100%", height: 1 },
    softbreak: {},
    pre: {},
    inline: {},
    span: {},
    fileCitation: { color: theme.colors.accent, backgroundColor: theme.colors.surfaceAlt,
      borderRadius: 4, paddingHorizontal: 4, paddingVertical: 1, fontSize: 13 },
    codexCard: { width: "100%", marginTop: 2, marginBottom: blockGap, paddingHorizontal: 12,
      paddingVertical: 10, borderWidth: StyleSheet.hairlineWidth, borderColor: theme.colors.border,
      borderRadius: 10, backgroundColor: theme.colors.surfaceAlt },
    codexCardLabel: { color: theme.colors.textMuted, fontFamily: "Inter_400Regular", fontSize: 12,
      lineHeight: 18, marginBottom: 2 },
    codexCardTitle: { color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 15,
      lineHeight: 22 },
    codexCardBody: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 14,
      lineHeight: 21, marginTop: 5 },
    htmlUnderline: { color: theme.colors.text, textDecorationLine: "underline" },
    htmlSub: { color: theme.colors.text, fontSize: 11, lineHeight: 16 },
    htmlSup: { color: theme.colors.text, fontSize: 11, lineHeight: 16 },
  }), [blockGap, compact, theme]);
  const rules = useMemo<RenderRules>(() => ({
    ...selectableRules,
    text: (node, _children, parents, styles, inheritedStyles = {}) =>
      renderMarkdownText(node, [], parents, styles, inheritedStyles, onFileCitationPress),
    paragraph: (node, children, _parents, styles) => {
      const token = soleText(node)?.trim();
      const directive = token ? lookupMarkdownPlaceholder(token) : null;
      if (directive && directive.kind !== "file-citation" && directive.kind !== "html-inline") {
        return <MarkdownDirectiveCard key={node.key} directive={directive} styles={styles} />;
      }
      return lightweightTextBlock(node, children, _parents, styles);
    },
    image: (node) => {
      const source = String(node.attributes.src ?? "");
      const filename = String(node.attributes.alt || source.split("/").at(-1) || "图片");
      if (source.startsWith("/")) {
        return <RemoteMessageImage key={node.key} profileId={profileId} remotePath={source}
          filename={filename} cacheKey={`markdown:${cacheKey}:${node.key}`}
          testID={`${imageTestPrefix}:${node.key}`} />;
      }
      return <CachedMessageImage key={node.key} uri={source} filename={filename}
        cacheKey={`markdown:${cacheKey}:${node.key}`}
        testID={`${imageTestPrefix}:${node.key}`} />;
    },
  }), [cacheKey, imageTestPrefix, onFileCitationPress, profileId]);
  if (!ast) {
    const preview = prepared.source.length > 4_000
      ? `${prepared.source.slice(0, 4_000)}…` : prepared.source;
    return <Text selectable style={[markdownStyle.text, compact && { opacity: 0.78 }]}>
      {preview}
    </Text>;
  }
  return <Markdown markdownit={markdownIt} mergeStyle={false} rules={rules} style={markdownStyle}>
    {ast as unknown as ReactNode}
  </Markdown>;
});

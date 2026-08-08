import { memo, type ReactNode, useMemo } from "react";
import { Platform, StyleSheet, Text } from "react-native";
import Markdown, { type ASTNode, MarkdownIt, parser, type RenderFunction,
  type RenderRules } from "react-native-markdown-display";

import { useTheme } from "@/theme/ThemeProvider";
import { CachedMessageImage } from "@/features/images/CachedMessageImage";

type MarkdownContentProps = {
  children: string;
  cacheKey: string;
  compact?: boolean;
  imageTestPrefix?: string;
};

const monoFont = Platform.select({ ios: "Menlo", android: "monospace", default: "monospace" });
const markdownIt = new MarkdownIt({ typographer: true });
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

const selectableTextGroup: RenderFunction = (node, children, _parents, styles) =>
  <Text key={node.key} selectable style={styles.textgroup}>{children}</Text>;

const selectableCode: RenderFunction = (node, _children, _parents, styles, inheritedStyles = {}) => {
  const content = node.content.endsWith("\n") ? node.content.slice(0, -1) : node.content;
  return <Text key={node.key} selectable style={[inheritedStyles, styles[node.type]]}>{content}</Text>;
};

const selectableRules: RenderRules = {
  textgroup: selectableTextGroup,
  code_block: selectableCode,
  fence: selectableCode,
};

export const MarkdownContent = memo(function MarkdownContent({ children, cacheKey, compact = false,
  imageTestPrefix = "markdown:image" }: MarkdownContentProps) {
  const theme = useTheme();
  const ast = useMemo(() => cachedMarkdownAst(cacheKey, children), [cacheKey, children]);
  const blockGap = compact ? 5 : 8;
  const markdownStyle = useMemo(() => StyleSheet.create({
    body: { width: "100%" },
    text: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    textgroup: { color: theme.colors.text, fontFamily: "Inter_400Regular", fontSize: 15, lineHeight: 24 },
    paragraph: { width: "100%", flexDirection: "row", flexWrap: "wrap", alignItems: "flex-start",
      marginTop: 0, marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_400Regular",
      fontSize: 15, lineHeight: 24 },
    heading1: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 8 : 12,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 24,
      lineHeight: 32 },
    heading2: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 7 : 10,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 21,
      lineHeight: 29 },
    heading3: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: compact ? 6 : 8,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 18,
      lineHeight: 26 },
    heading4: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 6,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 16,
      lineHeight: 24 },
    heading5: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 4,
      marginBottom: blockGap, color: theme.colors.text, fontFamily: "Inter_600SemiBold", fontSize: 15,
      lineHeight: 23 },
    heading6: { width: "100%", flexDirection: "row", flexWrap: "wrap", marginTop: 4,
      marginBottom: blockGap, color: theme.colors.textMuted, fontFamily: "Inter_600SemiBold", fontSize: 14,
      lineHeight: 22 },
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
  }), [blockGap, compact, theme]);
  const rules = useMemo<RenderRules>(() => ({
    ...selectableRules,
    image: (node) => {
      const source = String(node.attributes.src ?? "");
      const filename = String(node.attributes.alt || source.split("/").at(-1) || "图片");
      return <CachedMessageImage key={node.key} uri={source} filename={filename}
        testID={`${imageTestPrefix}:${node.key}`} />;
    },
  }), [imageTestPrefix]);
  return <Markdown markdownit={markdownIt} mergeStyle={false} rules={rules} style={markdownStyle}>
    {ast as unknown as ReactNode}
  </Markdown>;
});

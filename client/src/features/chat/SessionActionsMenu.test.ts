import { beforeEach, expect, it, vi } from "vitest";

const fixture = vi.hoisted(() => ({
  state: { activeConnection: { engine: "pi" }, threads: [{ thread: { id: "t", name: "Pi", preview: "", status: { type: "idle" } }, archived: false }],
    interruptThread: vi.fn(), setThreadArchived: vi.fn(), renameThread: vi.fn(), deleteThread: vi.fn().mockResolvedValue(undefined) },
  alert: vi.fn(),
}));
vi.mock("react", async (original) => ({ ...await original<typeof import("react")>(), useState: (value: unknown) => [value, vi.fn()] }));
vi.mock("react-native", () => ({ Alert: { alert: fixture.alert }, Modal: "Modal", Pressable: "Pressable", Text: "Text", TextInput: "TextInput", View: "View",
  StyleSheet: { create: (s: unknown) => s, absoluteFill: {} } }));
vi.mock("react-native-safe-area-context", () => ({ useSafeAreaInsets: () => ({ top: 0 }) }));
vi.mock("@/components/ui", () => ({ Button: "Button", Title: "Title" }));
vi.mock("@/theme/ThemeProvider", () => ({ useTheme: () => ({ colors: {}, shadow: {} }) }));
vi.mock("@/store/appStore", () => ({ useAppStore: (select: (state: unknown) => unknown) => select(fixture.state) }));

import { SessionActionsMenu } from "./SessionActionsMenu";

type Element = { props?: { children?: unknown; testID?: string; disabled?: boolean; onPress?: () => void } };
function find(node: unknown, id: string): Element | undefined {
  if (Array.isArray(node)) return node.map(child => find(child, id)).find(Boolean);
  if (!node || typeof node !== "object") return undefined;
  const element = node as Element;
  return element.props?.testID === id ? element : find(element.props?.children, id);
}

beforeEach(() => { vi.clearAllMocks(); fixture.state.activeConnection.engine = "pi"; fixture.state.threads[0]!.thread.status.type = "idle"; });

it("Pi 隐藏归档入口，删除需确认且调用删除而非归档", async () => {
  const tree = SessionActionsMenu({ sessionId: "t", onArchiveViewChange: vi.fn() });
  expect(find(tree, "session:archive")).toBeUndefined();
  expect(find(tree, "sessions:view-archived")).toBeUndefined();
  const remove = find(tree, "session:delete")!;
  expect(remove.props!.disabled).toBe(false);
  remove.props!.onPress!();
  expect(fixture.state.deleteThread).not.toHaveBeenCalled();
  const buttons = fixture.alert.mock.calls[0]![2];
  buttons[1].onPress();
  await vi.waitFor(() => expect(fixture.state.deleteThread).toHaveBeenCalledWith("t"));
  expect(fixture.state.setThreadArchived).not.toHaveBeenCalled();
});

it("Pi 活动会话不能删除，Codex 仍显示归档", () => {
  fixture.state.threads[0]!.thread.status.type = "active";
  expect(find(SessionActionsMenu({ sessionId: "t" }), "session:delete")?.props?.disabled).toBe(true);
  fixture.state.activeConnection.engine = "codex";
  const tree = SessionActionsMenu({ sessionId: "t" });
  expect(find(tree, "session:archive")).toBeDefined();
  expect(find(tree, "session:delete")).toBeUndefined();
});

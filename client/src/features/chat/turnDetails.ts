import type { OfficialItemPage } from "@/app-server/officialClient";
import type { MobileThreadItem, MobileTurn } from "@/app-server/types";
import { visibleTurnAnswers } from "./turnAnswers";

export type TurnDetailState = {
  expansion?: { status: MobileTurn["status"]; expanded: boolean };
  items: MobileThreadItem[];
  direction: "asc" | "desc";
  loaded: boolean;
  nextCursor: string | null;
  loading: boolean;
  error: string | null;
  baselineIds: ReadonlySet<string>;
  pageBaseline: ReadonlyMap<string, MobileThreadItem>;
};

export type DetailLoader = (turnId: string, cursor: string | null,
  direction: "asc" | "desc") => Promise<OfficialItemPage>;

export function turnExpanded(turn: MobileTurn, detail?: TurnDetailState): boolean {
  return detail?.expansion?.status === turn.status
    ? detail.expansion.expanded : turn.status === "inProgress";
}

/** 详情仅属于当前页面实例；不将部分 Item 页冒充完整 Turn 写回历史缓存。 */
export class TurnDetails {
  private state: ReadonlyMap<string, TurnDetailState> = new Map();
  private readonly listeners = new Set<() => void>();
  private readonly requests = new Map<string, Promise<void>>();
  private readonly cursors = new Map<string, Set<string>>();
  private generation = 0;

  constructor(private readonly loader: DetailLoader) {}

  snapshot = (): ReadonlyMap<string, TurnDetailState> => this.state;
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  dispose(): void {
    this.generation += 1;
    this.requests.clear();
    this.state = new Map();
    this.cursors.clear();
  }

  toggle(turn: MobileTurn, paginated: boolean): void {
    const current = this.get(turn);
    const expanded = !turnExpanded(turn, this.state.get(turn.id));
    this.set(turn.id, { ...current, expansion: { status: turn.status, expanded } });
    if (expanded && paginated && !current.loaded) void this.load(turn);
  }

  load(turn: MobileTurn): Promise<void> {
    const pending = this.requests.get(turn.id);
    if (pending) return pending;
    const before = this.get(turn);
    if (before.loaded && before.nextCursor === null) return Promise.resolve();
    const cursor = before.loaded ? before.nextCursor : null;
    const generation = this.generation;
    this.set(turn.id, { ...before, loading: true, error: null });
    // 延后调用 loader，确保同步异常也走局部错误状态，且请求登记先于响应。
    const promise = Promise.resolve().then(() => this.loader(turn.id, cursor, before.direction))
      .then((page) => {
        if (generation !== this.generation) return;
        const seen = this.cursors.get(turn.id) ?? new Set<string>();
        if (cursor !== null) seen.add(cursor);
        if (page.nextCursor !== null && seen.has(page.nextCursor)) {
          throw new Error("详情分页返回了重复游标");
        }
        this.cursors.set(turn.id, seen);
        const current = this.get(turn);
        const ordered = before.direction === "desc"
          ? [...page.items, ...current.items] : [...current.items, ...page.items];
        const items = [...new Map(ordered.map((item) => [item.id, item])).values()];
        const liveAtRequest = new Map(turn.items.map((item) => [item.id, item]));
        const pageBaseline = new Map(current.pageBaseline);
        for (const item of page.items) {
          const baseline = liveAtRequest.get(item.id);
          if (baseline) pageBaseline.set(item.id, baseline);
        }
        this.set(turn.id, { ...current, items, loaded: true, loading: false,
          nextCursor: page.nextCursor, error: null, pageBaseline });
      }).catch((error: unknown) => {
        if (generation !== this.generation) return;
        this.set(turn.id, { ...this.get(turn), loading: false,
          error: error instanceof Error ? error.message : "加载本轮内容失败" });
      }).finally(() => {
        if (this.requests.get(turn.id) === promise) this.requests.delete(turn.id);
      });
    this.requests.set(turn.id, promise);
    return promise;
  }

  private get(turn: MobileTurn): TurnDetailState {
    return this.state.get(turn.id) ?? { items: [], loaded: false, nextCursor: null,
      baselineIds: new Set(turn.items.map((item) => item.id)),
      pageBaseline: new Map(),
      direction: turn.status === "inProgress" ? "desc" : "asc", loading: false, error: null };
  }

  private set(id: string, value: TurnDetailState): void {
    this.state = new Map(this.state).set(id, value);
    this.listeners.forEach((listener) => listener());
  }
}

/** 已收到的实时内容优先于稍后返回的历史页；摘要两端不打乱详情中间的顺序。 */
export function turnWithDetails(turn: MobileTurn, detail?: TurnDetailState): MobileTurn {
  if (!detail) return turn;
  const live = new Map(turn.items.map((item) => [item.id, item]));
  const loadedIds = new Set(detail.items.map((item) => item.id));
  const items = detail.items.map((item) => {
    const current = live.get(item.id);
    // 请求开始后变更的实时 Item 优先；未变化的旧内存快照不能覆盖新详情页。
    return current && current !== detail.pageBaseline.get(item.id) ? current : item;
  });
  const firstUser = turn.items.find((item) => item.type === "userMessage");
  const answers = new Set(visibleTurnAnswers(turn).map((item) => item.id));
  const missing = turn.items.filter((item) => !loadedIds.has(item.id) &&
    (item === firstUser || answers.has(item.id) ||
      !detail.baselineIds.has(item.id)));
  const prefix = missing.filter((item) => item === firstUser);
  const suffix = missing.filter((item) => item !== firstUser);
  return { ...turn, items: [...prefix, ...items, ...suffix],
    // 正序已到底或倒序已到头，才拥有这轮完整的已知历史。
    itemsView: detail.loaded && detail.nextCursor === null ? "full" : "summary" };
}

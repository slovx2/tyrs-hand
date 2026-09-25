import type { Model } from "@codex-app-server/v2/Model";
import { z } from "zod";

// 固定 0.147.0 的 JSON Schema 允许省略这些字段；生成的 TS 类型已经应用默认值。
// 在协议边界落实同一套默认值，避免界面把合法的 wire 数据直接当作完整 Model。
const catalogDefaults = z.object({
  modelSpecialty: z.string().nullable().default(null),
  serviceTiers: z.array(z.object({
    id: z.string(), name: z.string(), description: z.string(),
  })).default([]),
  defaultServiceTier: z.string().nullable().default(null),
});

export function decodeModelCatalogEntry(model: Model): Model {
  return { ...model, ...catalogDefaults.parse(model) };
}

import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import { Ajv, type AnySchema } from "ajv";
import { Ajv2020 } from "ajv/dist/2020";
import addFormats from "ajv-formats";

export type McpField = {
  name: string; title: string; description: string; required: boolean;
  kind: "string" | "number" | "integer" | "boolean" | "enum" | "multiEnum" | "json";
  options: { label: string; value: JsonValue }[];
  defaultValue?: JsonValue;
};
export type McpValidation = { valid: true } | { valid: false; message: string };
export type McpForm = {
  fields: McpField[]; jsonMode: boolean; schemaText: string;
  error: string | null; validate: (content: JsonValue) => McpValidation;
};

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown> : null;
}
function options(schema: Record<string, unknown>): McpField["options"] {
  if (Array.isArray(schema.enum)) {
    return schema.enum.map((value, index) => ({ value: value as JsonValue,
      label: Array.isArray(schema.enumNames) && typeof schema.enumNames[index] === "string"
        ? schema.enumNames[index] : String(value) }));
  }
  const alternatives = schema.oneOf ?? schema.anyOf;
  if (Array.isArray(alternatives) && alternatives.every((entry) =>
    object(entry) && Object.hasOwn(entry, "const"))) {
    return alternatives.map((entry) => {
      const choice = entry as Record<string, unknown>;
      return { value: choice.const as JsonValue,
        label: typeof choice.title === "string" ? choice.title : String(choice.const) };
    });
  }
  return [];
}

export function prepareMcpForm(schema: unknown): McpForm {
  const definition = object(schema);
  const schemaText = JSON.stringify(schema, null, 2) ?? "null";
  const invalid = (message: string): McpForm => ({ fields: [], jsonMode: true, schemaText,
    error: message, validate: () => ({ valid: false, message }) });
  if (!definition && typeof schema !== "boolean") return invalid("服务端表单结构无效。");
  try {
    // 每张请求卡片独立编译；不加载远端引用，不修改用户输入，也不自动补默认值。
    const config = { strict: false, strictNumbers: true, allErrors: true, coerceTypes: false, useDefaults: false,
      removeAdditional: false };
    const validator = typeof definition?.$schema === "string" && definition.$schema.includes("draft-07")
      ? new Ajv(config) : new Ajv2020(config);
    addFormats(validator);
    const compiled = validator.compile(schema as AnySchema);
    if ("$async" in compiled && compiled.$async) return invalid("不支持需要异步校验的表单。");
    const properties = object(definition?.properties);
    const required = Array.isArray(definition?.required) ? definition.required : [];
    const fields = Object.entries(properties ?? {}).map(([name, raw]): McpField => {
      const field = object(raw) ?? {};
      const choice = options(field);
      const itemChoices = field.type === "array" ? options(object(field.items) ?? {}) : [];
      const kind: McpField["kind"] = choice.length ? "enum" : itemChoices.length ? "multiEnum" :
        field.type === "string" || field.type === "number" || field.type === "integer" || field.type === "boolean"
          ? field.type : "json";
      return { name, title: typeof field.title === "string" ? field.title : name,
        description: typeof field.description === "string" ? field.description : "",
        required: required.includes(name), kind, options: choice.length ? choice : itemChoices,
        ...(Object.hasOwn(field, "default") ? { defaultValue: field.default as JsonValue } : {}) };
    });
    const composite = ["allOf", "anyOf", "oneOf", "if", "$ref", "patternProperties"]
      .some((key) => definition && Object.hasOwn(definition, key));
    return { fields, schemaText, jsonMode: definition?.type !== "object" || fields.length === 0 || composite,
      error: null, validate: (content) => {
        if (compiled(content)) return { valid: true };
        return { valid: false, message: (compiled.errors ?? []).slice(0, 4)
          .map((error) => `${error.instancePath || "表单"}：${error.message ?? "输入不符合要求"}`).join("\n") };
      } };
  } catch {
    return invalid("无法校验此表单，请拒绝或取消，并让服务端提供有效的完整结构。");
  }
}

export function initialMcpValues(fields: McpField[]): Record<string, JsonValue> {
  return Object.fromEntries(fields.filter((field) => field.defaultValue !== undefined)
    .map((field) => [field.name, JSON.parse(JSON.stringify(field.defaultValue)) as JsonValue]));
}

export function parseMcpJson(text: string): JsonValue {
  return JSON.parse(text, (_key, value: unknown) => {
    if (typeof value === "number" && !Number.isFinite(value)) throw new Error("JSON 数字超出有效范围。");
    return value;
  }) as JsonValue;
}

export function parseMcpText(field: McpField, text: string):
  { valid: true; value: JsonValue | undefined } | { valid: false; message: string } {
  if (text === "" && !field.required) return { valid: true, value: undefined };
  if (field.kind === "string") return { valid: true, value: text };
  if (field.kind === "number" || field.kind === "integer") {
    const value = Number(text);
    if (!text.trim() || !Number.isFinite(value) ||
      !/^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/.test(text.trim()) ||
      (field.kind === "integer" && !Number.isInteger(value))) {
      return { valid: false, message: `${field.title}需要有效的${field.kind === "integer" ? "整数" : "数字"}。` };
    }
    return { valid: true, value };
  }
  try {
    return { valid: true, value: parseMcpJson(text) };
  } catch {
    return { valid: false, message: `${field.title}需要有效的 JSON。` };
  }
}

export function collectMcpValues(fields: McpField[], values: Record<string, JsonValue>,
  texts: Record<string, string>): { valid: true; value: Record<string, JsonValue> } | { valid: false; message: string } {
  const next = { ...values };
  for (const field of fields) {
    if (!Object.hasOwn(texts, field.name)) continue;
    const result = parseMcpText(field, texts[field.name] ?? "");
    if (!result.valid) return result;
    if (result.value === undefined) delete next[field.name];
    else Object.defineProperty(next, field.name, { value: result.value, enumerable: true, configurable: true, writable: true });
  }
  return { valid: true, value: next };
}

export function mcpUrl(url: string): string | null {
  try {
    const parsed = new URL(url);
    return ["https:", "http:"].includes(parsed.protocol) && parsed.hostname &&
      !parsed.username && !parsed.password ? parsed.href : null;
  } catch {
    return null;
  }
}

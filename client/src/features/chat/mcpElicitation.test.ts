import { describe, expect, it } from "vitest";

import { collectMcpValues, initialMcpValues, mcpUrl, parseMcpJson, parseMcpText, prepareMcpForm } from "./mcpElicitation";

describe("MCP 手机表单", () => {
  it("提交时合并可见输入并删除选填空值，保留 false 和数字零", () => {
    const form = prepareMcpForm({ type: "object", properties: {
      enabled: { type: "boolean" }, amount: { type: "number" }, note: { type: "string" },
    } });
    expect(collectMcpValues(form.fields, { enabled: false, note: "旧输入" }, { amount: "0", note: "" }))
      .toEqual({ valid: true, value: { enabled: false, amount: 0 } });
    expect(collectMcpValues(form.fields, {}, { amount: "invalid" }).valid).toBe(false);
  });
  it("完整 JSON 保留嵌套类型，拒绝会在发送时被静默改成 null 的数字", () => {
    expect(parseMcpJson('{"values":[false,0,null,""]}')).toEqual({ values: [false, 0, null, ""] });
    expect(() => parseMcpJson('{"values":[1e999]}')).toThrow();
  });
  it("保留字符串、数字零、整数、false、枚举及多选原生类型", () => {
    const form = prepareMcpForm({ type: "object", properties: {
      name: { type: "string" }, amount: { type: "number" }, count: { type: "integer" },
      enabled: { type: "boolean", default: false },
      choice: { type: "string", enum: ["a", "b"], enumNames: ["甲", "乙"] },
      colors: { type: "array", items: { anyOf: [{ const: "red", title: "红" }, { const: "blue", title: "蓝" }] } },
    }, additionalProperties: false });
    expect(form.error).toBeNull();
    expect(form.fields.map((field) => field.kind)).toEqual(["string", "number", "integer", "boolean", "enum", "multiEnum"]);
    expect(form.fields[4]?.options).toEqual([{ label: "甲", value: "a" }, { label: "乙", value: "b" }]);
    expect(form.fields[5]?.options[0]).toEqual({ label: "红", value: "red" });
    expect(parseMcpText(form.fields[0]!, "0")).toEqual({ valid: true, value: "0" });
    expect(parseMcpText(form.fields[1]!, "0")).toEqual({ valid: true, value: 0 });
    expect(parseMcpText(form.fields[2]!, "2.5").valid).toBe(false);
    expect(initialMcpValues(form.fields)).toEqual({ enabled: false });
    expect(form.validate({ name: "a", amount: 0, count: 1, enabled: false, choice: "a", colors: ["red"] }).valid).toBe(true);
    expect(form.validate({ enabled: "false" }).valid).toBe(false);
  });

  it("默认值可见且独立，不隐式填默认值或改写输入", () => {
    const form = prepareMcpForm({ type: "object", properties: {
      count: { type: "integer", default: 3 }, list: { type: "array", default: ["x"] },
    }, required: ["count"], additionalProperties: false });
    const first = initialMcpValues(form.fields);
    (first.list as string[]).push("changed");
    expect(initialMcpValues(form.fields)).toEqual({ count: 3, list: ["x"] });
    const missing = {};
    expect(form.validate(missing).valid).toBe(false);
    expect(missing).toEqual({});
    const invalid = { count: "3", extra: true };
    expect(form.validate(invalid).valid).toBe(false);
    expect(invalid).toEqual({ count: "3", extra: true });
  });

  it("选填清空成为缺省，空数字不变零，保留必填空字符串", () => {
    const form = prepareMcpForm({ type: "object", properties: {
      optional: { type: "string" }, number: { type: "number" }, text: { type: "string" },
    }, required: ["number", "text"] });
    expect(parseMcpText(form.fields[0]!, "")).toEqual({ valid: true, value: undefined });
    expect(parseMcpText(form.fields[1]!, "").valid).toBe(false);
    expect(parseMcpText(form.fields[2]!, "")).toEqual({ valid: true, value: "" });
    for (const invalid of ["0x10", "Infinity", "NaN", "+1", "01", "1e999"]) {
      expect(parseMcpText(form.fields[1]!, invalid).valid).toBe(false);
    }
  });

  it("严格校验 required、范围、pattern、email 和额外字段", () => {
    const form = prepareMcpForm({ type: "object", properties: {
      count: { type: "integer", minimum: 1, maximum: 3 },
      code: { type: "string", pattern: "^[A-Z]{2}$" }, email: { type: "string", format: "email" },
    }, required: ["count", "code", "email"], additionalProperties: false });
    const good = { count: 2, code: "OK", email: "test@example.invalid" };
    expect(form.validate(good).valid).toBe(true);
    for (const bad of [{ ...good, count: 0 }, { ...good, count: 4 }, { ...good, code: "bad" },
      { ...good, email: "invalid" }, { ...good, extra: true }, {}]) {
      expect(form.validate(bad).valid).toBe(false);
    }
  });

  it("支持嵌套 JSON、本地引用和 oneOf，复杂根使用完整 JSON 编辑", () => {
    const form = prepareMcpForm({ type: "object", $defs: {
      nested: { type: "object", properties: { value: { type: "number" } }, required: ["value"] },
    }, properties: { nested: { $ref: "#/$defs/nested" } }, required: ["nested"] });
    expect(form.error).toBeNull();
    expect(form.fields[0]?.kind).toBe("json");
    expect(parseMcpText(form.fields[0]!, '{"value":2}')).toEqual({ valid: true, value: { value: 2 } });
    expect(form.validate({ nested: { value: 2 } }).valid).toBe(true);
    expect(form.validate({ nested: { value: "2" } }).valid).toBe(false);
    const composite = prepareMcpForm({ type: "object", properties: { x: { type: "string" } },
      oneOf: [{ required: ["x"] }, { required: ["y"] }] });
    expect(composite.jsonMode).toBe(true);
    expect(composite.validate({ x: "x", y: true }).valid).toBe(false);
    expect(composite.validate({ y: false }).valid).toBe(true);
  });

  it("支持 draft07 和标量根，拒绝外部引用、异步与非法 schema", () => {
    const draft = prepareMcpForm({ $schema: "http://json-schema.org/draft-07/schema#", type: "string", minLength: 2 });
    expect(draft.error).toBeNull();
    expect(draft.jsonMode).toBe(true);
    expect(draft.validate("ok").valid).toBe(true);
    expect(draft.validate("x").valid).toBe(false);
    for (const schema of [null, [], { type: "not-a-type" }, { $ref: "https://example.invalid/schema" },
      { $async: true, type: "object" }]) {
      const invalid = prepareMcpForm(schema);
      expect(invalid.error).not.toBeNull();
      expect(invalid.validate({}).valid).toBe(false);
    }
  });
});

describe("MCP URL", () => {
  it("只允许无内嵌凭据的 HTTP(S) 链接", () => {
    expect(mcpUrl("https://example.invalid/confirm?id=42")).toBe("https://example.invalid/confirm?id=42");
    expect(mcpUrl("http://127.0.0.1:4000/")).toBe("http://127.0.0.1:4000/");
    for (const url of ["javascript:alert(1)", "file:///tmp/secret", "data:text/plain,secret",
      "https://name:secret@example.invalid/", "https://name@example.invalid/", "/relative", "invalid"]) {
      expect(mcpUrl(url)).toBeNull();
    }
  });
});

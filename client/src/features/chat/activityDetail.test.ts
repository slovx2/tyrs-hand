import { describe, expect, it } from "vitest";

import { activityDetailPreview } from "./activityDetail";

describe("活动详情预览", () => {
  it("保留短输出", () => {
    expect(activityDetailPreview("line 1\nline 2")).toEqual({
      text: "line 1\nline 2", truncated: false,
    });
  });

  it("只布局前 12 行并标记截断", () => {
    const detail = Array.from({ length: 20 }, (_, index) => `line ${index + 1}`).join("\n");
    const preview = activityDetailPreview(detail);

    expect(preview.text.split("\n")).toHaveLength(13);
    expect(preview.text).toContain("line 12");
    expect(preview.text).not.toContain("line 13");
    expect(preview.text.endsWith("…")).toBe(true);
    expect(preview.truncated).toBe(true);
  });

  it("限制单行超长输出", () => {
    const preview = activityDetailPreview("x".repeat(5_000));
    expect(preview.text).toHaveLength(4_002);
    expect(preview.text.endsWith("\n…")).toBe(true);
    expect(preview.truncated).toBe(true);
  });
});

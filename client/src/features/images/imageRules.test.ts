import { describe, expect, it } from "vitest";

import { classifyImageSource, decodeDataImage, detectImageType } from "./imageRules";

const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";

describe("图片来源规则", () => {
  it("只缓存网络和 data 图片，本机资源直接渲染", () => {
    expect(classifyImageSource("https://example.com/a.png")).toBe("network");
    expect(classifyImageSource("http://192.168.1.2/a.png")).toBe("network");
    expect(classifyImageSource(`data:image/png;base64,${png}`)).toBe("data");
    expect(classifyImageSource("file:///tmp/a.png")).toBe("local");
    expect(classifyImageSource("content://media/1")).toBe("local");
    expect(classifyImageSource("asset:/a.png")).toBe("local");
    expect(classifyImageSource("ftp://example.com/a.png")).toBe("unsupported");
    expect(classifyImageSource("relative/a.png")).toBe("unsupported");
  });

  it("校验 data 图片的 MIME、大小和真实图片签名", () => {
    const decoded = decodeDataImage(`data:image/png;base64,${png}`);
    expect(decoded.mediaType).toBe("image/png");
    expect(detectImageType(decoded.bytes)).toBe("image/png");
    expect(() => decodeDataImage("data:text/plain;base64,SGVsbG8=")).toThrow("无效");
    expect(() => decodeDataImage(`data:image/png;base64,${png}`, 4)).toThrow("大小限制");
    expect(detectImageType(new TextEncoder().encode("not an image"))).toBeNull();
  });
});

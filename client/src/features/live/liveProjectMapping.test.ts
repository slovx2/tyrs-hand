import { describe, expect, it } from "vitest";

import { normalizeAbsolutePath, resolveLiveProjectForSSHProject } from "./liveProjectMapping";

const project = (id: string, hostPath: string) => ({
  id, name: id, relativePath: `workspaces/${id}`, hostPath, availabilityStatus: "available",
});

describe("SSH 与 Control 项目映射", () => {
  it("规范化绝对路径后精确匹配", () => {
    const result = resolveLiveProjectForSSHProject(
      { cwd: "/workspace/app/../app/" }, [project("control-app", "/workspace/app")]);
    expect(result).toMatchObject({ status: "matched", project: { id: "control-app" } });
    expect(normalizeAbsolutePath("/workspace/./app/")).toBe("/workspace/app");
  });

  it("同名但路径不同不能匹配", () => {
    const result = resolveLiveProjectForSSHProject(
      { cwd: "/other/app" }, [project("app", "/workspace/app")]);
    expect(result.status).toBe("unmatched");
  });

  it("多个相同路径返回不唯一", () => {
    const result = resolveLiveProjectForSSHProject(
      { cwd: "/workspace/app" }, [project("one", "/workspace/app"), project("two", "/workspace/app")]);
    expect(result.status).toBe("ambiguous");
  });

  it("缺少 hostPath 时不猜测", () => {
    const result = resolveLiveProjectForSSHProject(
      { cwd: "/workspace/app" }, [project("app", "")]);
    expect(result.status).toBe("unmatched");
  });
});

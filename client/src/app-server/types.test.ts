import type { Thread } from "@codex-app-server/v2/Thread";
import { describe, expect, it } from "vitest";

import { projectForThread, type MobileProject } from "./types";

function project(id: string, cwd: string): MobileProject {
  return { id, cwd, workspaceId: null, name: id, relativePath: cwd, kind: "ssh",
    availabilityStatus: "available", branch: null, dirty: false };
}

function thread(cwd: string): Thread {
  return { cwd } as Thread;
}

describe("projectForThread", () => {
  it("assigns a thread to the most specific SSH project root", () => {
    const root = project("root", "/workspace");
    const nested = project("nested", "/workspace/tyrs-hand");

    expect(projectForThread([root, nested], thread("/workspace/tyrs-hand/client")))
      .toBe(nested);
    expect(projectForThread([root, nested], thread("/workspace/another"))).toBe(root);
  });

  it("treats the filesystem root as an ancestor of every absolute cwd", () => {
    const root = project("root", "/");

    expect(projectForThread([root], thread("/workspace/project"))).toBe(root);
  });
});

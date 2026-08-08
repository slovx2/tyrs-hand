import { beforeEach, describe, expect, it, vi } from "vitest";

import { addSSHProject, listSSHProjects, removeSSHProject } from "./sshProjects";

const database = vi.hoisted(() => ({
  getAllAsync: vi.fn(),
  runAsync: vi.fn(),
}));

vi.mock("expo-crypto", () => ({ randomUUID: () => "project-1" }));
vi.mock("./database", () => ({
  getDatabase: async () => database,
  runDatabaseWrite: async (callback: (value: typeof database) => Promise<unknown>) =>
    callback(database),
}));

describe("SSH projects persistence", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    database.getAllAsync.mockResolvedValue([]);
    database.runAsync.mockResolvedValue({ changes: 1 });
  });

  it("adds a normalized absolute project without changing the SSH connection", async () => {
    const project = await addSSHProject("profile-1", "/workspace/project/");

    expect(project).toEqual({ profileId: "profile-1", id: "project-1",
      remotePath: "/workspace/project" });
    expect(database.runAsync).toHaveBeenCalledWith(expect.stringContaining("ssh_projects"),
      "profile-1", "project-1", "/workspace/project", expect.any(String), expect.any(String));
    await expect(addSSHProject("profile-1", "relative/path"))
      .rejects.toThrow("项目目录必须是绝对路径");
  });

  it("rejects adding the same remote path twice", async () => {
    database.runAsync.mockResolvedValueOnce({ changes: 0 });

    await expect(addSSHProject("profile-1", "/workspace/project"))
      .rejects.toThrow("该目录已经添加为项目");
  });

  it("lists and removes projects within one SSH profile", async () => {
    database.getAllAsync.mockResolvedValueOnce([{ profile_id: "profile-1", id: "project-1",
      remote_path: "/workspace/project" }]);

    await expect(listSSHProjects("profile-1")).resolves.toEqual([{
      profileId: "profile-1", id: "project-1", remotePath: "/workspace/project",
    }]);
    await removeSSHProject("profile-1", "project-1");
    expect(database.runAsync).toHaveBeenLastCalledWith(
      "DELETE FROM ssh_projects WHERE profile_id=? AND id=?", "profile-1", "project-1");
  });
});

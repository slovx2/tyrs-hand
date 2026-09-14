import type { MobileProject } from "@/app-server/types";
import type { LiveWorkerProject } from "@/api/live";

export type LiveProjectResolution =
  | { status: "matched"; project: LiveWorkerProject }
  | { status: "unmatched"; project: null; message: string }
  | { status: "ambiguous"; project: null; message: string };

export function resolveLiveProjectForSSHProject(
  sshProject: Pick<MobileProject, "cwd"> | null,
  controlProjects: LiveWorkerProject[],
): LiveProjectResolution {
  if (!sshProject) {
    return { status: "unmatched", project: null, message: "请先选择 Codex 项目" };
  }
  const sshPath = normalizeAbsolutePath(sshProject.cwd);
  if (!sshPath) {
    return { status: "unmatched", project: null, message: "SSH 项目路径无效" };
  }
  const matches = controlProjects.filter((project) =>
    project.availabilityStatus === "available" &&
    normalizeAbsolutePath(project.hostPath) === sshPath);
  if (matches.length === 1) return { status: "matched", project: matches[0]! };
  if (matches.length > 1) {
    return { status: "ambiguous", project: null, message: "Control 项目路径匹配不唯一" };
  }
  return { status: "unmatched", project: null, message: "Control 项目尚未同步，请先重新扫描 Worker 项目" };
}

export function normalizeAbsolutePath(value: string): string | null {
  const raw = value.trim().replace(/\\/g, "/");
  if (!raw.startsWith("/")) return null;
  const parts: string[] = [];
  for (const part of raw.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (parts.length > 0) parts.pop();
      continue;
    }
    parts.push(part);
  }
  return `/${parts.join("/")}` || "/";
}

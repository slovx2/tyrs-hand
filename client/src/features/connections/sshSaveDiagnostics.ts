export type SSHSaveStage = "inspect-key" | "probe-host" | "confirm-host" |
  "prepare-profile" | "inspect-runtime" | "store-private-key" | "store-passphrase" |
  "write-profile" | "cleanup-old-credentials" | "reload-profiles" | "activate-profile";
export type SSHSaveStatus = "running" | "completed" | "failed" | "timeout" | "cancelled";
export type SSHSaveProgress = { stage: SSHSaveStage; status: SSHSaveStatus; elapsedMs: number; errorCode?: string };

const labels: Record<SSHSaveStage, string> = {
  "inspect-key": "检查私钥", "probe-host": "探测主机指纹", "confirm-host": "等待确认主机指纹",
  "prepare-profile": "准备连接标识", "inspect-runtime": "检查远端身份",
  "store-private-key": "保存私钥凭据", "store-passphrase": "保存口令凭据",
  "write-profile": "写入连接信息", "cleanup-old-credentials": "清理旧凭据",
  "reload-profiles": "刷新连接列表", "activate-profile": "切换连接",
};

function errorCode(error: unknown): string {
  const value = error && typeof error === "object" && "code" in error ? error.code : undefined;
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  if (typeof value === "string" && /^(?:ERR_|E_|SSH_|SQLITE_)[A-Z0-9_]{1,64}$/.test(value)) return value;
  const names = ["Error", "TypeError", "RangeError", "SyntaxError", "AbortError", "TimeoutError", "NativeError", "ZodError"];
  return error instanceof Error && names.includes(error.name) ? error.name : "UNKNOWN";
}

export function sshSaveProgressLabel(progress: SSHSaveProgress): string {
  const label = labels[progress.stage];
  if (progress.status === "failed") return `${label}失败（${progress.errorCode ?? "UNKNOWN"}）`;
  if (progress.status === "timeout") return `${label}仍未返回（已等待 ${Math.floor(progress.elapsedMs / 1000)} 秒）`;
  if (progress.status === "cancelled") return "已取消保存 SSH";
  return `${label}${progress.status === "completed" ? "完成" : "…"}`;
}

// 诊断只接收阶段和错误码，不接收私钥、口令、地址、配对 URI 或完整异常消息。
export function createSSHSaveDiagnostics(onProgress: (progress: SSHSaveProgress) => void, timeoutMs = 30_000) {
  let stage: SSHSaveStage | undefined;
  let startedAt = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const record = (status: SSHSaveStatus, error?: unknown) => {
    if (!stage) return;
    const progress: SSHSaveProgress = { stage, status, elapsedMs: Date.now() - startedAt,
      ...(status === "failed" ? { errorCode: errorCode(error) } : {}) };
    const log = status === "failed" || status === "timeout" ? console.error : console.info;
    log("[ssh-save]", JSON.stringify(progress));
    onProgress(progress);
  };
  const finish = (status: SSHSaveStatus, error?: unknown) => {
    clearTimeout(timer);
    timer = undefined;
    record(status, error);
    stage = undefined;
  };
  return {
    start(next: SSHSaveStage) {
      finish("completed");
      stage = next; startedAt = Date.now();
      record("running");
      // 用户确认不能被操作计时替代；其他阶段超时仅诊断，不中断可能已经生效的写入。
      if (next !== "confirm-host") timer = setTimeout(() => record("timeout"), timeoutMs);
    },
    complete: () => finish("completed"),
    cancel: () => finish("cancelled"),
    fail: (error: unknown) => finish("failed", error),
  };
}

import type { ModuleInstallation } from "./types";

// Pure helpers shared by the admin console and the device admin PWA.
// Keep this file free of runtime imports so `node --test` can load it directly.

export const INSTALLATION_SWITCH_HINT =
  "同一 Key 可在多台手机间切换：在哪台手机打开微信，哪台就接管；在用手机离线约 2 分钟后，在线的待命手机自动接管。";

export const INSTALLATION_SWITCH_REQUESTED =
  "已请求切换，目标手机下一次心跳（约 10 秒内）接管；10 分钟内未上线则作废";

export const INSTALLATION_ALREADY_ACTIVE = "这台手机已是在用手机，列表已刷新";

export type InstallationTone = "success" | "outline" | "secondary" | "destructive";

/** 在用 / 在用·离线 / 待命 / 离线 */
export function installationStateLabel(item: Pick<ModuleInstallation, "active" | "state">) {
  if (item.active) return item.state === "offline" ? "在用·离线" : "在用";
  return item.state === "offline" ? "离线" : "待命";
}

export function installationStateTone(item: Pick<ModuleInstallation, "active" | "state">): InstallationTone {
  if (item.active) return item.state === "offline" ? "destructive" : "success";
  return item.state === "offline" ? "secondary" : "outline";
}

export function installationIsStandby(item: Pick<ModuleInstallation, "active" | "state">) {
  return !item.active && item.state !== "offline";
}

/** Old module versions do not report a device model. */
export function installationModelText(item: Pick<ModuleInstallation, "device_model">) {
  return item.device_model?.trim() || "旧版模块";
}

export function installationShortId(item: Pick<ModuleInstallation, "id" | "short_id">) {
  return `#${item.short_id || item.id}`;
}

export function installationDisplayName(item: Pick<ModuleInstallation, "id" | "short_id" | "device_model">) {
  return `${installationModelText(item)} ${installationShortId(item)}`;
}

export function installationVersionsText(
  item: Pick<ModuleInstallation, "android_version" | "wechat_version" | "module_version">
) {
  return [
    item.android_version ? `Android ${item.android_version}` : "",
    item.wechat_version ? `微信 ${item.wechat_version}` : "",
    item.module_version ? `模块 ${item.module_version}` : ""
  ]
    .filter(Boolean)
    .join(" · ");
}

export function installationAccountText(item: Pick<ModuleInstallation, "wechat_nickname" | "owner_wxid">) {
  const nickname = item.wechat_nickname?.trim() || "";
  const wxid = item.owner_wxid?.trim() || "";
  if (nickname && wxid && nickname !== wxid) return `${nickname}（${wxid}）`;
  return nickname || wxid;
}

export function installationSwitchPending(
  item: Pick<ModuleInstallation, "id" | "switch_pending">,
  switchRequest?: { installation_id: number }
) {
  return item.switch_pending || switchRequest?.installation_id === item.id;
}

export type ApiErrorInfo = { code: string; message: string };

/**
 * The API helpers throw `new Error(responseText)`; the server error body is
 * `{"ok":false,"code":"...","message":"..."}`. Extract code + message, falling
 * back to the raw text for plain-text errors.
 */
export function parseApiError(error: unknown, fallback: string): ApiErrorInfo {
  const raw = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  if (!raw.trim()) return { code: "", message: fallback };
  try {
    const payload: unknown = JSON.parse(raw);
    if (payload && typeof payload === "object") {
      const record = payload as Record<string, unknown>;
      const code = typeof record.code === "string" ? record.code : "";
      const message =
        typeof record.message === "string" && record.message
          ? record.message
          : typeof record.error === "string"
            ? record.error
            : "";
      if (code || message) return { code, message: message || code };
    }
  } catch {
    // Plain-text error body.
  }
  return { code: "", message: raw };
}

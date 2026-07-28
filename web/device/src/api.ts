import type { DeviceApiKey, DeviceModule } from "./types";

type RequestOptions = {
  password: string;
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
};

const OBSERVATORY_MOUNT = "/observatory";

function publicPath(path: string) {
  const mounted = window.location.pathname === OBSERVATORY_MOUNT
    || window.location.pathname.startsWith(`${OBSERVATORY_MOUNT}/`);
  return `${mounted ? OBSERVATORY_MOUNT : ""}${path}`;
}

async function requestJSON<T>(path: string, options: RequestOptions): Promise<T> {
  const headers: Record<string, string> = {
    "X-Bridge-Device-Password": options.password
  };
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(publicPath(path), {
    method: options.method ?? "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body)
  });
  const text = await response.text();
  if (!response.ok) throw new Error(text || `HTTP ${response.status}`);
  return JSON.parse(text) as T;
}

export function getDeviceModules(password: string) {
  return requestJSON<{ modules: DeviceModule[] }>("/api/device-admin/modules", { password });
}

export function getDeviceApiKeys(password: string) {
  return requestJSON<{ api_keys: DeviceApiKey[] }>("/api/device-admin/api-keys?limit=200", { password });
}

export function createDeviceApiKey(params: { password: string; apiKey?: string; device?: string; nickname?: string }) {
  return requestJSON<{ ok: boolean; api_key: DeviceApiKey }>("/api/device-admin/api-keys", {
    password: params.password,
    method: "POST",
    body: { api_key: params.apiKey, device: params.device, nickname: params.nickname }
  });
}

export function setDeviceApiKeyEnabled(params: { password: string; apiKey: string; enabled: boolean }) {
  const action = params.enabled ? "enable" : "disable";
  return requestJSON<{ ok: boolean; api_key: DeviceApiKey }>(
    `/api/device-admin/api-keys/${encodeURIComponent(params.apiKey)}/${action}`,
    { password: params.password, method: "POST" }
  );
}

export function deleteDeviceApiKey(params: { password: string; apiKey: string }) {
  return requestJSON<{ ok: boolean }>(`/api/device-admin/api-keys/${encodeURIComponent(params.apiKey)}`, {
    password: params.password,
    method: "DELETE"
  });
}

export function updateDeviceName(params: { password: string; name: string; nickname: string }) {
  return requestJSON<{ ok: boolean; device: DeviceModule }>("/api/device-admin/devices", {
    password: params.password,
    method: "POST",
    body: { name: params.name, nickname: params.nickname }
  });
}

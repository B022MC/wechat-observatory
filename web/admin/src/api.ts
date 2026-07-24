import type { ApiKey, LiveMessageEvent, ModuleContact, ModuleStatus, StoredMessage } from "@/types";

type ApiOptions = {
  password: string;
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
};

const OBSERVATORY_MOUNT = "/observatory";

export function publicPath(path: string) {
  if (!path.startsWith("/")) {
    throw new Error("public path must start with /");
  }
  const mounted = window.location.pathname === OBSERVATORY_MOUNT
    || window.location.pathname.startsWith(`${OBSERVATORY_MOUNT}/`);
  return `${mounted ? OBSERVATORY_MOUNT : ""}${path}`;
}

async function requestJSON<T>(path: string, options: ApiOptions): Promise<T> {
  const headers: Record<string, string> = { "X-Bridge-Password": options.password };
  if (options.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const response = await fetch(publicPath(path), {
    method: options.method ?? "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body)
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(text || `HTTP ${response.status}`);
  }
  return JSON.parse(text) as T;
}

export async function getModules(password: string) {
  return requestJSON<{ modules: ModuleStatus[] }>("/api/modules/status", { password });
}

export async function getApiKeys(password: string) {
  return requestJSON<{ api_keys?: ApiKey[] }>("/api/api-keys?limit=200", { password });
}

export async function createApiKey(params: {
  password: string;
  apiKey?: string;
  device?: string;
  nickname?: string;
}) {
  return requestJSON<{ ok: boolean; api_key?: ApiKey }>("/api/api-keys", {
    password: params.password,
    method: "POST",
    body: {
      api_key: params.apiKey,
      device: params.device,
      nickname: params.nickname
    }
  });
}

export async function deleteApiKey(params: { password: string; apiKey: string }) {
  return requestJSON<{ ok: boolean }>(`/api/api-keys/${encodeURIComponent(params.apiKey)}`, {
    password: params.password,
    method: "DELETE"
  });
}

export async function setApiKeyEnabled(params: { password: string; apiKey: string; enabled: boolean }) {
  return requestJSON<{ ok: boolean; api_key?: ApiKey }>(
    `/api/api-keys/${encodeURIComponent(params.apiKey)}/${params.enabled ? "enable" : "disable"}`,
    {
      password: params.password,
      method: "POST"
    }
  );
}

export async function updateDevice(params: { password: string; name: string; nickname?: string }) {
  return requestJSON<{ ok: boolean; device: ModuleStatus }>("/api/devices", {
    password: params.password,
    method: "POST",
    body: {
      name: params.name,
      nickname: params.nickname
    }
  });
}

export async function getContacts(params: {
  password: string;
  device: string;
  ownerWxid?: string;
  query: string;
  includeDeleted: boolean;
  limit?: number;
}) {
  const search = new URLSearchParams({
    device: params.device,
    limit: String(params.limit ?? 300)
  });
  if (params.query.trim()) {
    search.set("q", params.query.trim());
  }
  if (params.ownerWxid?.trim()) {
    search.set("owner_wxid", params.ownerWxid.trim());
  }
  if (params.includeDeleted) {
    search.set("include_deleted", "1");
  }
  return requestJSON<{ contacts: ModuleContact[] }>(`/api/module-contacts?${search}`, {
    password: params.password
  });
}

export async function getMessages(params: {
  password: string;
  device: string;
  wxid?: string;
  ownerWxid?: string;
  chatId?: string;
  chatKind?: string;
  limit?: number;
}) {
  const search = new URLSearchParams({
    device: params.device,
    limit: String(params.limit ?? 120)
  });
  if (params.chatId) {
    search.set("chat_id", params.chatId);
  }
  if (params.ownerWxid?.trim()) {
    search.set("owner_wxid", params.ownerWxid.trim());
  }
  if (params.chatKind) {
    search.set("chat_kind", params.chatKind);
  }
  if (params.wxid) {
    search.set("wxid", params.wxid);
  }
  return requestJSON<{ messages: StoredMessage[] }>(`/api/messages?${search}`, {
    password: params.password
  });
}

export async function sendText(params: {
  password: string;
  device: string;
  ownerWxid: string;
  wxid: string;
  text: string;
}) {
  return requestJSON<{ ok: boolean; outbox_id?: number }>("/api/send/text", {
    password: params.password,
    method: "POST",
    body: {
      device: params.device,
      owner_wxid: params.ownerWxid,
      wx_ids: [params.wxid],
      text: params.text
    }
  });
}

export function openLiveEvents(password: string) {
  const search = new URLSearchParams({ password });
  return new EventSource(publicPath(`/api/live/events?${search}`));
}

export function parseLiveMessageEvent(raw: string) {
  return JSON.parse(raw) as LiveMessageEvent;
}

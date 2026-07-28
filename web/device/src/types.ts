export type DeviceModule = {
  device: string;
  device_wxid?: string;
  device_nickname?: string;
  enabled: boolean;
  runtime_status: "online" | "offline" | "disabled" | "unregistered" | string;
  last_seen_at?: string;
};

export type DeviceApiKey = {
  code: string;
  api_key?: string;
  device?: string;
  nickname?: string;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
};

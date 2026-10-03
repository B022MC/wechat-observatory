import type { ModuleInstallation, ModuleSwitchRequest } from "@/types";

export type { ModuleInstallation, ModuleSwitchRequest };

export type DeviceModule = {
  device: string;
  device_wxid?: string;
  device_nickname?: string;
  enabled: boolean;
  runtime_status: "online" | "offline" | "disabled" | "unregistered" | string;
  last_seen_at?: string;
  /** Phones that used this device binding, current phone first (absent on old servers). */
  installations?: ModuleInstallation[];
  switch_request?: ModuleSwitchRequest;
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

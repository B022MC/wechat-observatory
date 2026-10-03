export type ModuleStatus = {
  account_generation?: number;
  device: string;
  device_wxid?: string;
  device_nickname?: string;
  enabled?: boolean;
  runtime_status?: string;
  pending_outbox?: number;
  leased_outbox?: number;
  sent_outbox?: number;
  failed_outbox?: number;
  last_outbox_id?: number;
  last_outbox_status?: string;
  last_register_at?: string;
  last_poll_at?: string;
  last_ack_at?: string;
  last_poll_limit?: number;
  last_poll_item_count?: number;
  last_ack_sent_count?: number;
  last_ack_failed_count?: number;
  last_outbound_ack_at?: string;
  installations?: ModuleInstallation[];
  switch_request?: ModuleSwitchRequest;
};

/** One phone (installation) that used a device binding; the current phone is listed first. */
export type ModuleInstallation = {
  id: number;
  short_id: string;
  active: boolean;
  state: "active" | "standby" | "offline" | string;
  owner_wxid?: string;
  wechat_nickname?: string;
  device_model?: string;
  android_version?: string;
  wechat_version?: string;
  module_version?: string;
  last_seen_at?: string;
  last_active_at?: string;
  switch_pending: boolean;
};

/** Pending operator switch; applied on the target phone's next heartbeat. */
export type ModuleSwitchRequest = {
  installation_id: number;
  expires_at: string;
};

export type ApiKey = {
  code: string;
  api_key?: string;
  device?: string;
  nickname?: string;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
};

export type ModuleContact = {
  id: number;
  device: string;
  owner_wxid?: string;
  wxid: string;
  nickname?: string;
  remark?: string;
  alias?: string;
  type?: number;
  verify_flag?: number;
  chatroom: boolean;
  deleted: boolean;
  last_seen_at?: string;
  updated_at?: string;
};

export type StoredMessage = {
  id: number;
  source_id?: string;
  event_id?: number;
  chat_record_id?: number;
  device: string;
  chat_id?: string;
  chat_kind?: "direct" | "room" | string;
  owner_wxid?: string;
  direction: "recv" | "sent" | string;
  from_wxid?: string;
  to_wxid?: string;
  room_id?: string;
  sender_wxid?: string;
  text: string;
  message_type: number;
  media_kind?: string;
  media_mime?: string;
  media_name?: string;
  media_url?: string;
  media_size?: number;
  raw_provider?: string;
  create_time?: number;
  created_at?: string;
};

export type LiveMessageEvent = {
  owner_wxid?: string;
  id?: string;
  event_id?: number;
  chat_record_id?: number;
  device: string;
  chat_id?: string;
  chat_kind?: "direct" | "room" | string;
  direction: "recv" | "sent" | string;
  from?: string;
  to?: string;
  room_id?: string;
  sender?: string;
  text: string;
  message_type?: number;
  media_kind?: string;
  media_mime?: string;
  media_name?: string;
  media_url?: string;
  media_size?: number;
  raw_provider?: string;
  create_time?: number;
};

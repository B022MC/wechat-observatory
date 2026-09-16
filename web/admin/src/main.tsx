import React from "react";
import { createRoot } from "react-dom/client";
import { formatBeijingClock, formatBeijingDateTime, formatBeijingTimeAgo } from "@/time";
import {
  Activity,
  CheckCircle2,
  Download,
  FileText,
  Hash,
  ImageIcon,
  KeyRound,
  MessageCircle,
  Mic,
  Moon,
  Paperclip,
  PauseCircle,
  RefreshCw,
  Search,
  Send,
  ShieldCheck,
  Smartphone,
  PlayCircle,
  Sun,
  Trash2,
  UserRound,
  Users,
  Video,
  Wifi,
  WifiOff
} from "lucide-react";
import {
  createApiKey,
  deleteApiKey,
  getContacts,
  getApiKeys,
  getMessages,
  getModules,
  openLiveEvents,
  parseLiveMessageEvent,
  publicPath,
  setApiKeyEnabled,
  sendText,
  updateDevice
} from "@/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import type { ApiKey, LiveMessageEvent, ModuleContact, ModuleStatus, StoredMessage } from "@/types";
import "./index.css";

const PASSWORD_KEY = "wgc_admin_password";
const THEME_KEY = "wgc_admin_theme";
const RAW_PROVIDER_MODULE_ACK = "module_ack";
const CONTACT_SNAPSHOT_LIMIT = 10000;

type ContactFilter = "all" | "direct" | "room" | "messages";
type ThemeMode = "light" | "dark";

function resolveTheme(): ThemeMode {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored === "light" || stored === "dark") {
    return stored;
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function App() {
  const [password, setPassword] = React.useState(() => localStorage.getItem(PASSWORD_KEY) || "");
  const [theme, setTheme] = React.useState<ThemeMode>(() => resolveTheme());
  const [modules, setModules] = React.useState<ModuleStatus[]>([]);
  const [apiKeys, setApiKeys] = React.useState<ApiKey[]>([]);
  const [contacts, setContacts] = React.useState<ModuleContact[]>([]);
  const [messages, setMessages] = React.useState<StoredMessage[]>([]);
  const [recentMessages, setRecentMessages] = React.useState<StoredMessage[]>([]);
  const [selectedDevice, setSelectedDevice] = React.useState("");
  const [selectedWxid, setSelectedWxid] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [includeDeleted, setIncludeDeleted] = React.useState(false);
  const [contactFilter, setContactFilter] = React.useState<ContactFilter>("all");
  const [autoRefresh, setAutoRefresh] = React.useState(true);
  const [liveConnected, setLiveConnected] = React.useState(false);
  const [sendOpen, setSendOpen] = React.useState(false);
  const [draft, setDraft] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [sending, setSending] = React.useState(false);
  const [savingAdmin, setSavingAdmin] = React.useState(false);
  const [newApiKey, setNewApiKey] = React.useState("");
  const [newApiKeyDevice, setNewApiKeyDevice] = React.useState("");
  const [newApiKeyNickname, setNewApiKeyNickname] = React.useState("");
  const [deviceNicknameDraft, setDeviceNicknameDraft] = React.useState("");
  const [notice, setNotice] = React.useState("输入管理密码后连接");

  const adminPassword = password.trim();
  const selectedModule = React.useMemo(
    () => modules.find((item) => item.device === selectedDevice),
    [modules, selectedDevice]
  );
  const selectedOwnerWxid = moduleOwnerWxid(selectedModule);
  const selectedScopeKey = `${selectedDevice}:${selectedOwnerWxid}`;
  const selectedScopeRef = React.useRef(selectedScopeKey);
  const messageListRef = React.useRef<HTMLDivElement>(null);
  const loadedMessageChatRef = React.useRef("");
  const pendingBottomScrollChatRef = React.useRef("");

  React.useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem(THEME_KEY, theme);
  }, [theme]);

  React.useEffect(() => {
    selectedScopeRef.current = selectedScopeKey;
  }, [selectedScopeKey]);

  React.useEffect(() => {
    setDeviceNicknameDraft(selectedModule?.device_nickname || selectedModule?.device || "");
  }, [selectedModule?.device, selectedModule?.device_nickname]);

  const visibleContacts = React.useMemo(() => {
    return contacts.filter((item) => {
      if (contactFilter === "direct") return !isRoomContact(item);
      if (contactFilter === "room") return isRoomContact(item);
      return true;
    });
  }, [contactFilter, contacts]);

  const contactByWxid = React.useMemo(() => {
    const map = new Map<string, ModuleContact>();
    contacts.forEach((item) => map.set(item.wxid, item));
    return map;
  }, [contacts]);

  const visibleRecentMessages = React.useMemo(() => {
    const conversations = new Map<string, StoredMessage>();
    recentMessages.forEach((item) => {
      const chatId = messageChatId(item);
      if (!chatId || conversations.has(chatId)) return;
      const contact = chatId ? contactByWxid.get(chatId) : undefined;
      if (contact?.deleted) return;
      const room = messageIsRoom(item, contact);
      if (contactFilter === "direct" && room) return false;
      if (contactFilter === "room" && !room) return false;
      if (query.trim()) {
        const needle = query.trim().toLowerCase();
        const haystack = [
          contact ? contactName(contact) : "",
          contact ? contactSecondary(contact) : "",
          messageContactName(item, contact),
          messagePreview(item)
        ]
          .join(" ")
          .toLowerCase();
        if (!haystack.includes(needle)) return false;
      }
      conversations.set(chatId, item);
    });
    return Array.from(conversations.values());
  }, [contactByWxid, contactFilter, query, recentMessages]);

  const selectedContact = React.useMemo(
    () => contacts.find((item) => item.wxid === selectedWxid),
    [contacts, selectedWxid]
  );

  const pendingCount = selectedModule?.pending_outbox ?? 0;
  const failedCount = selectedModule?.failed_outbox ?? 0;
  const sentCount = selectedModule?.sent_outbox ?? 0;
  const selectedModuleOffline = selectedModule?.runtime_status === "offline";
  const messageListActive = contactFilter === "messages";
  const contactQuery = messageListActive ? "" : query;
  const contactIncludeDeleted = messageListActive ? true : includeDeleted;
  const leftListCountText = messageListActive ? `${visibleRecentMessages.length} 个会话` : `${visibleContacts.length} 个对象`;
  const leftListTitle = messageListActive ? "消息列表" : "好友列表";
  const leftSearchPlaceholder = messageListActive ? "搜索消息、昵称、备注" : "搜索昵称、备注、别名";
  const scopeMatches = React.useCallback((device: string, ownerWxid: string) => {
    return selectedScopeRef.current === `${device}:${ownerWxid || ""}`;
  }, []);

  const refreshApiKeys = React.useCallback(async () => {
    if (!adminPassword) {
      setApiKeys([]);
      return [] as ApiKey[];
    }
    const payload = await getApiKeys(adminPassword);
    const nextKeys = payload.api_keys || [];
    setApiKeys(nextKeys);
    return nextKeys;
  }, [adminPassword]);

  const refreshModules = React.useCallback(async () => {
    if (!adminPassword) {
      setNotice("请输入管理密码");
      return [] as ModuleStatus[];
    }
    const payload = await getModules(adminPassword);
    const nextModules = payload.modules || [];
    setModules(nextModules);
    setSelectedDevice((current) => {
      if (current && nextModules.some((item) => item.device === current)) return current;
      return nextModules[0]?.device || "";
    });
    return nextModules;
  }, [adminPassword]);

  const refreshContacts = React.useCallback(
    async (device: string, ownerWxid = selectedOwnerWxid) => {
      if (!adminPassword || !device) {
        setContacts([]);
        setSelectedWxid("");
        return [] as ModuleContact[];
      }
      const payload = await getContacts({
        password: adminPassword,
        device,
        ownerWxid,
        query: contactQuery,
        includeDeleted: contactIncludeDeleted,
        limit: CONTACT_SNAPSHOT_LIMIT
      });
      if (!scopeMatches(device, ownerWxid)) {
        return [] as ModuleContact[];
      }
      const nextContacts = payload.contacts || [];
      setContacts(nextContacts);
      setSelectedWxid((current) => {
        if (current && nextContacts.some((item) => item.wxid === current)) return current;
        return nextContacts[0]?.wxid || "";
      });
      return nextContacts;
    },
    [adminPassword, contactIncludeDeleted, contactQuery, scopeMatches, selectedOwnerWxid]
  );

  const refreshMessages = React.useCallback(
    async (device: string, wxid: string, ownerWxid = selectedOwnerWxid) => {
      if (!adminPassword || !device || !wxid) {
        loadedMessageChatRef.current = "";
        setMessages([]);
        return [] as StoredMessage[];
      }
      const contact = contacts.find((item) => item.wxid === wxid);
      const payload = await getMessages({
        password: adminPassword,
        device,
        wxid,
        ownerWxid,
        chatId: wxid,
        chatKind: chatKindForContact(wxid, contact),
        limit: 180
      });
      if (!scopeMatches(device, ownerWxid)) {
        return [] as StoredMessage[];
      }
      const nextMessages = (payload.messages || []).slice().reverse();
      loadedMessageChatRef.current = wxid;
      setMessages(nextMessages);
      return nextMessages;
    },
    [adminPassword, contacts, scopeMatches, selectedOwnerWxid]
  );

  const refreshRecentMessages = React.useCallback(
    async (device: string, ownerWxid = selectedOwnerWxid) => {
      if (!adminPassword || !device) {
        setRecentMessages([]);
        return [] as StoredMessage[];
      }
      const payload = await getMessages({
        password: adminPassword,
        device,
        ownerWxid,
        limit: 500
      });
      if (!scopeMatches(device, ownerWxid)) {
        return [] as StoredMessage[];
      }
      const nextMessages = payload.messages || [];
      setRecentMessages(nextMessages);
      return nextMessages;
    },
    [adminPassword, scopeMatches, selectedOwnerWxid]
  );

  const refreshAll = React.useCallback(async () => {
    setLoading(true);
    setNotice("正在连接服务端");
    try {
      localStorage.setItem(PASSWORD_KEY, adminPassword);
      const nextModules = await refreshModules();
      const device = nextModules.some((item) => item.device === selectedDevice)
        ? selectedDevice
        : nextModules[0]?.device || "";
      const ownerWxid = moduleOwnerWxid(nextModules.find((item) => item.device === device));
      selectedScopeRef.current = `${device}:${ownerWxid}`;
      if (device) {
        setSelectedDevice(device);
      }
      await refreshApiKeys();
      const nextContacts = await refreshContacts(device, ownerWxid);
      if (device) {
        await refreshRecentMessages(device, ownerWxid);
      }
      const wxid = nextContacts.some((item) => item.wxid === selectedWxid)
        ? selectedWxid
        : nextContacts[0]?.wxid || "";
      if (device && wxid) {
        await refreshMessages(device, wxid, ownerWxid);
      }
      setNotice(`已刷新 ${formatBeijingClock()}`);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "请求失败");
    } finally {
      setLoading(false);
    }
  }, [adminPassword, refreshApiKeys, refreshContacts, refreshMessages, refreshModules, refreshRecentMessages, selectedDevice, selectedWxid]);

  React.useEffect(() => {
    loadedMessageChatRef.current = "";
    pendingBottomScrollChatRef.current = "";
    setContacts([]);
    setMessages([]);
    setRecentMessages([]);
    setSelectedWxid("");
  }, [selectedScopeKey]);

  React.useEffect(() => {
    if (!selectedDevice) {
      setContacts([]);
      return;
    }
    const timer = window.setTimeout(() => {
      void refreshContacts(selectedDevice, selectedOwnerWxid).catch((error) => {
        setNotice(error instanceof Error ? error.message : "通讯录刷新失败");
      });
    }, 220);
    return () => window.clearTimeout(timer);
  }, [includeDeleted, refreshContacts, selectedDevice, selectedOwnerWxid]);

  React.useEffect(() => {
    if (visibleContacts.length === 0) {
      if (selectedWxid && !contacts.some((item) => item.wxid === selectedWxid)) {
        setSelectedWxid("");
      }
      return;
    }
    if (!selectedWxid || !visibleContacts.some((item) => item.wxid === selectedWxid)) {
      setSelectedWxid(visibleContacts[0].wxid);
    }
  }, [contacts, selectedWxid, visibleContacts]);

  React.useEffect(() => {
    if (!selectedDevice || !selectedWxid) {
      setMessages([]);
      return;
    }
    void refreshMessages(selectedDevice, selectedWxid, selectedOwnerWxid).catch((error) => {
      setNotice(error instanceof Error ? error.message : "消息刷新失败");
    });
  }, [refreshMessages, selectedDevice, selectedOwnerWxid, selectedWxid]);

  React.useLayoutEffect(() => {
    const messageList = messageListRef.current;
    if (
      !messageList ||
      !selectedWxid ||
      messages.length === 0 ||
      loadedMessageChatRef.current !== selectedWxid ||
      pendingBottomScrollChatRef.current !== selectedWxid
    ) {
      return;
    }
    messageList.scrollTop = messageList.scrollHeight;
    pendingBottomScrollChatRef.current = "";
  }, [messages, selectedWxid]);

  React.useEffect(() => {
    if (!selectedDevice) {
      setRecentMessages([]);
      return;
    }
    void refreshRecentMessages(selectedDevice, selectedOwnerWxid).catch((error) => {
      setNotice(error instanceof Error ? error.message : "消息列表刷新失败");
    });
  }, [refreshRecentMessages, selectedDevice, selectedOwnerWxid]);

  React.useEffect(() => {
    if (!autoRefresh || !selectedDevice || !selectedWxid) return;
    const timer = window.setInterval(() => {
      void refreshMessages(selectedDevice, selectedWxid, selectedOwnerWxid).catch(() => undefined);
      void refreshRecentMessages(selectedDevice, selectedOwnerWxid).catch(() => undefined);
      void refreshModules().catch(() => undefined);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [autoRefresh, refreshMessages, refreshModules, refreshRecentMessages, selectedDevice, selectedOwnerWxid, selectedWxid]);

  React.useEffect(() => {
    if (!adminPassword || !autoRefresh) {
      setLiveConnected(false);
      return;
    }
    const source = openLiveEvents(adminPassword);
    source.addEventListener("ready", () => {
      setLiveConnected(true);
    });
    source.addEventListener("message", (event) => {
      try {
        const payload = parseLiveMessageEvent((event as globalThis.MessageEvent).data);
        if (payload.device !== selectedDevice) return;
        void refreshModules().catch(() => undefined);
        if (payload.raw_provider === RAW_PROVIDER_MODULE_ACK) return;
        void refreshContacts(selectedDevice, selectedOwnerWxid).catch(() => undefined);
        void refreshRecentMessages(selectedDevice, selectedOwnerWxid).catch(() => undefined);
        if (selectedWxid && liveEventTouchesChat(payload, selectedWxid)) {
          void refreshMessages(selectedDevice, selectedWxid, selectedOwnerWxid).catch(() => undefined);
          setNotice(`实时消息 ${formatBeijingClock()}`);
        }
      } catch {
        // Keep the stream open if one event cannot be parsed.
      }
    });
    source.onerror = () => {
      setLiveConnected(false);
    };
    return () => {
      source.close();
      setLiveConnected(false);
    };
  }, [adminPassword, autoRefresh, refreshContacts, refreshMessages, refreshModules, refreshRecentMessages, selectedDevice, selectedOwnerWxid, selectedWxid]);

  const selectDevice = (device: string) => {
    const nextOwnerWxid = moduleOwnerWxid(modules.find((item) => item.device === device));
    selectedScopeRef.current = `${device}:${nextOwnerWxid}`;
    setSelectedDevice(device);
    setContacts([]);
    setMessages([]);
    setRecentMessages([]);
    setSelectedWxid("");
    loadedMessageChatRef.current = "";
    pendingBottomScrollChatRef.current = "";
  };

  const selectChat = (wxid: string) => {
    pendingBottomScrollChatRef.current = wxid;
    setSelectedWxid(wxid);
    if (selectedWxid === wxid && loadedMessageChatRef.current === wxid) {
      window.requestAnimationFrame(() => {
        const messageList = messageListRef.current;
        if (messageList && pendingBottomScrollChatRef.current === wxid) {
          messageList.scrollTop = messageList.scrollHeight;
          pendingBottomScrollChatRef.current = "";
        }
      });
    }
  };

  const cancelPendingBottomScroll = () => {
    if (pendingBottomScrollChatRef.current === selectedWxid) {
      pendingBottomScrollChatRef.current = "";
    }
  };

  const selectContact = (contact: ModuleContact) => {
    selectChat(contact.wxid);
  };

  const selectRecentMessage = (message: StoredMessage) => {
    const chatId = messageChatId(message);
    if (chatId) {
      selectChat(chatId);
    }
  };

  const openSendDialog = (contact?: ModuleContact) => {
	if (selectedModuleOffline) {
	  setNotice("当前微信离线，不能创建发送任务");
	  return;
	}
    if (contact) {
      setSelectedWxid(contact.wxid);
    }
    setDraft("");
    setSendOpen(true);
  };

  const submitSend = async () => {
    if (!selectedDevice || !selectedWxid || !draft.trim() || selectedModuleOffline) {
	  if (selectedModuleOffline) setNotice("当前微信离线，不能创建发送任务");
	  return;
	}
    setSending(true);
    setNotice("正在加入发送队列");
    try {
      const sentDevice = selectedDevice;
      const sentWxid = selectedWxid;
      await sendText({
        password: adminPassword,
        device: sentDevice,
        ownerWxid: selectedOwnerWxid,
        wxid: sentWxid,
        text: draft.trim()
      });
      setDraft("");
      setSendOpen(false);
      setNotice("消息已加入模块发送队列");
      void refreshModules().catch(() => undefined);
      window.setTimeout(() => {
        void refreshMessages(sentDevice, sentWxid, selectedOwnerWxid).catch(() => undefined);
        void refreshModules().catch(() => undefined);
      }, 800);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "发送失败");
    } finally {
      setSending(false);
    }
  };

  const submitApiKey = async () => {
    if (!adminPassword) return;
    setSavingAdmin(true);
    try {
      const payload = await createApiKey({
        password: adminPassword,
        apiKey: newApiKey.trim(),
        device: newApiKeyDevice.trim(),
        nickname: newApiKeyNickname.trim()
      });
      const created = payload.api_key;
      if (!created) {
        throw new Error("服务端没有返回 API Key");
      }
      setApiKeys((current) => [created, ...current.filter((item) => item.code !== created.code)]);
      setNewApiKey("");
      setNewApiKeyNickname("");
      setNotice(`已生成 API Key ${created.api_key || created.code}`);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "生成 API Key 失败");
    } finally {
      setSavingAdmin(false);
    }
  };

  const toggleApiKeyEnabled = async (apiKey: ApiKey) => {
    if (!adminPassword || !apiKey.code) return;
    setSavingAdmin(true);
    try {
      const payload = await setApiKeyEnabled({
        password: adminPassword,
        apiKey: apiKey.api_key || apiKey.code,
        enabled: !apiKey.enabled
      });
      const updated = payload.api_key;
      if (!updated) {
        throw new Error("服务端没有返回 API Key");
      }
      setApiKeys((current) => [updated, ...current.filter((item) => item.code !== updated.code)]);
      await refreshModules();
      setNotice(updated.enabled ? "API Key 已启用" : "API Key 已停用");
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "更新 API Key 状态失败");
    } finally {
      setSavingAdmin(false);
    }
  };

  const revokeApiKey = async (apiKey: string) => {
    if (!adminPassword || !apiKey) return;
    const ok = window.confirm(`确认删除并注销 API Key ${apiKey}？`);
    if (!ok) return;
    setSavingAdmin(true);
    try {
      await deleteApiKey({ password: adminPassword, apiKey });
      setApiKeys((current) => current.filter((item) => (item.api_key || item.code) !== apiKey));
      await refreshModules();
      setNotice("API Key 已删除");
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "删除 API Key 失败");
    } finally {
      setSavingAdmin(false);
    }
  };

  const submitDeviceNickname = async () => {
    if (!adminPassword || !selectedDevice) return;
    setSavingAdmin(true);
    try {
      await updateDevice({
        password: adminPassword,
        name: selectedDevice,
        nickname: deviceNicknameDraft.trim()
      });
      await refreshModules();
      setNotice("设备显示名已保存");
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "保存设备失败");
    } finally {
      setSavingAdmin(false);
    }
  };

  return (
    <div className="min-h-screen bg-muted/30 text-foreground">
      <header className="sticky top-0 z-30 border-b border-border/70 bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/80">
        <div className="mx-auto flex max-w-[1560px] flex-wrap items-center justify-between gap-3 px-4 py-3">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground">
              <ShieldCheck className="h-5 w-5" />
            </div>
            <div className="min-w-0">
              <h1 className="truncate text-lg font-semibold tracking-normal">微信看台</h1>
              <div className="mt-1 flex flex-wrap items-center gap-2">
                <ConnectionBadge connected={liveConnected} enabled={autoRefresh} />
                <StatusBadge status={selectedModule?.runtime_status} />
                <span className="max-w-[360px] truncate text-xs text-muted-foreground">{notice}</span>
              </div>
            </div>
          </div>

          <div className="flex w-full flex-wrap items-center gap-2 md:w-auto">
            <Button
              type="button"
              variant="outline"
              size="icon"
              aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
              onClick={() => setTheme((current) => (current === "dark" ? "light" : "dark"))}
            >
              {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
            <div className="relative min-w-[240px] flex-1 md:w-[320px] md:flex-none">
              <KeyRound className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
              <Input
                className="pl-8"
                type="password"
                placeholder="管理密码"
                value={password}
                onChange={(event: React.ChangeEvent<HTMLInputElement>) => setPassword(event.target.value)}
                onKeyDown={(event: React.KeyboardEvent<HTMLInputElement>) => {
                  if (event.key === "Enter") void refreshAll();
                }}
              />
            </div>
            <Button onClick={() => void refreshAll()} disabled={loading || !adminPassword}>
              <RefreshCw className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
              连接刷新
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto grid w-full min-w-0 max-w-[1600px] gap-4 p-3 lg:p-4">
        <aside className="grid h-fit gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Smartphone className="h-4 w-4" />
                手机模块
              </CardTitle>
              <Badge variant="secondary">{modules.length} 台</Badge>
            </CardHeader>
            <CardContent className="grid gap-2">
              {modules.length === 0 ? (
                <EmptyState icon={<Smartphone className="h-5 w-5" />} text="未读取到模块状态" />
              ) : (
                modules.map((item) => (
                  <button
                    key={item.device}
                    className={
                      item.device === selectedDevice
                        ? "rounded-lg border border-primary bg-secondary p-3 text-left shadow-sm"
                        : "rounded-lg border bg-card p-3 text-left transition hover:bg-secondary"
                    }
                    onClick={() => selectDevice(item.device)}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="truncate font-medium">{item.device || "-"}</div>
                        <div className="truncate text-xs text-muted-foreground">
                          {item.device_nickname || "未上报微信信息"}
                        </div>
                      </div>
                      <StatusBadge status={item.runtime_status} />
                    </div>
                    <div className="mt-3 grid grid-cols-2 gap-2 text-xs text-muted-foreground">
                      <span>待发：{item.pending_outbox ?? 0}</span>
                      <span>失败：{item.failed_outbox ?? 0}</span>
                    </div>
                  </button>
                ))
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Activity className="h-4 w-4" />
                运行信号
              </CardTitle>
            </CardHeader>
            <CardContent className="grid gap-3 text-sm">
              <label className="flex items-center gap-2">
                <Checkbox checked={autoRefresh} onChange={(event) => setAutoRefresh(event.currentTarget.checked)} />
                实时流和轮询刷新
              </label>
              <div className="grid grid-cols-3 gap-2">
                <SignalPill label="注册" value={formatBeijingTimeAgo(selectedModule?.last_register_at)} />
                <SignalPill label="拉取" value={formatBeijingTimeAgo(selectedModule?.last_poll_at)} />
                <SignalPill label="回执" value={formatBeijingTimeAgo(selectedModule?.last_ack_at)} />
              </div>
            </CardContent>
          </Card>
        </aside>

        <section className="grid min-w-0 gap-4">
          <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-4">
            <MetricCard title="当前设备" value={selectedModule?.device || "-"} icon={<Smartphone className="h-4 w-4" />} />
            <MetricCard title="运行状态" value={statusText(selectedModule?.runtime_status)} icon={<Activity className="h-4 w-4" />} />
            <MetricCard title="发送成功" value={String(sentCount)} icon={<CheckCircle2 className="h-4 w-4" />} />
            <MetricCard title="通讯录" value={`${visibleContacts.length}/${contacts.length}`} icon={<Users className="h-4 w-4" />} />
          </div>

          <div className="grid min-w-0 gap-4 lg:grid-cols-[420px_minmax(0,1fr)] xl:grid-cols-[440px_minmax(0,1fr)]">
            <Card className="min-w-0">
              <CardHeader className="items-start">
                <div className="grid gap-1">
                  <CardTitle className="flex items-center gap-2">
                    {messageListActive ? <MessageCircle className="h-4 w-4" /> : <Users className="h-4 w-4" />}
                    {leftListTitle}
                  </CardTitle>
                  <div className="text-xs text-muted-foreground">
                    {selectedDevice || "未选择设备"} · {leftListCountText}
                  </div>
                </div>
                <Badge variant={includeDeleted ? "warning" : "secondary"}>{includeDeleted ? "含删除" : "有效"}</Badge>
              </CardHeader>
              <CardContent className="grid gap-3">
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
                  <Input
                    className="pl-8"
                    value={query}
                    onChange={(event: React.ChangeEvent<HTMLInputElement>) => setQuery(event.target.value)}
                    placeholder={leftSearchPlaceholder}
                  />
                </div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Tabs className="gap-0">
                    <TabsList>
                      <TabsTrigger active={contactFilter === "all"} onClick={() => setContactFilter("all")}>
                        全部
                      </TabsTrigger>
                      <TabsTrigger active={contactFilter === "direct"} onClick={() => setContactFilter("direct")}>
                        好友
                      </TabsTrigger>
                      <TabsTrigger active={contactFilter === "room"} onClick={() => setContactFilter("room")}>
                        群聊
                      </TabsTrigger>
                      <TabsTrigger active={contactFilter === "messages"} onClick={() => setContactFilter("messages")}>
                        消息列表
                      </TabsTrigger>
                    </TabsList>
                  </Tabs>
                  <label className="flex h-9 items-center gap-2 rounded-md border px-3 text-sm">
                    <Checkbox
                      checked={includeDeleted}
                      onChange={(event: React.ChangeEvent<HTMLInputElement>) => setIncludeDeleted(event.currentTarget.checked)}
                    />
                    含删除
                  </label>
                </div>

                {messageListActive ? (
                  <div className="grid max-h-[620px] min-h-[420px] content-start gap-2 overflow-y-auto overflow-x-hidden pr-1">
                    {visibleRecentMessages.length === 0 ? (
                      <EmptyState
                        icon={<MessageCircle className="h-5 w-5" />}
                        text={selectedDevice ? "当前筛选暂无消息" : "请先连接并选择模块"}
                      />
                    ) : (
                      visibleRecentMessages.slice(0, 80).map((item) => {
                        const chatId = messageChatId(item);
                        const contact = chatId ? contactByWxid.get(chatId) : undefined;
                        return (
                          <button
                            key={`recent:${chatId}`}
                            type="button"
                            className={
                              chatId === selectedWxid
                                ? "w-full rounded-lg border border-primary bg-secondary p-3 text-left shadow-sm"
                                : "w-full rounded-lg border bg-card p-3 text-left transition hover:bg-secondary"
                            }
                            onClick={() => selectRecentMessage(item)}
                          >
                            <div className="flex min-w-0 items-center justify-between gap-2">
                              <div className="min-w-0 truncate text-sm font-medium">{messageContactName(item, contact)}</div>
                              <span className="shrink-0 text-[11px] text-muted-foreground">{formatBeijingTimeAgo(item.created_at)}</span>
                            </div>
                            <div className="mt-1 truncate text-xs text-muted-foreground">
                              {messagePreview(item, contactByWxid, selectedModule)}
                            </div>
                          </button>
                        );
                      })
                    )}
                  </div>
                ) : (
                <div className="grid max-h-[620px] min-h-[420px] content-start gap-2 overflow-y-auto overflow-x-hidden pr-1">
                  {visibleContacts.length === 0 ? (
                    <EmptyState
                      icon={<Users className="h-5 w-5" />}
                      text={selectedDevice ? "当前筛选没有好友" : "请先连接并选择模块"}
                    />
                  ) : (
                    visibleContacts.map((item) => (
                      <button
                        key={`${item.device}:${item.wxid}`}
                        className={
                          item.wxid === selectedWxid
                            ? "w-full rounded-lg border border-primary bg-secondary p-3 text-left shadow-sm"
                            : "w-full rounded-lg border bg-card p-3 text-left transition hover:bg-secondary"
                        }
                        onClick={() => selectContact(item)}
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="min-w-0">
                            <div className="flex min-w-0 items-center gap-2">
                              {isRoomContact(item) ? (
                                <Hash className="h-4 w-4 shrink-0 text-muted-foreground" />
                              ) : (
                                <UserRound className="h-4 w-4 shrink-0 text-muted-foreground" />
                              )}
                              <span className="truncate font-medium">{contactName(item)}</span>
                            </div>
                            <div className="mt-1 truncate text-xs text-muted-foreground">{contactSecondary(item)}</div>
                          </div>
                          <Badge variant={item.deleted ? "destructive" : isRoomContact(item) ? "warning" : "success"}>
                            {item.deleted ? "已删除" : isRoomContact(item) ? "群聊" : "好友"}
                          </Badge>
                        </div>
                        <div className="mt-3 text-xs text-muted-foreground">
                          <span className="truncate">上报：{formatBeijingTimeAgo(item.last_seen_at || item.updated_at)}</span>
                        </div>
                      </button>
                    ))
                  )}
                </div>
                )}
              </CardContent>
            </Card>

            <Card className="min-w-0">
              <CardHeader className="items-start md:items-center">
                <div className="min-w-0">
                  <CardTitle className="flex items-center gap-2">
                    <MessageCircle className="h-4 w-4" />
                    消息列表
                  </CardTitle>
                  <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span className="max-w-[420px] truncate">{selectedChatTitle(selectedWxid, selectedContact)}</span>
                    {selectedContact ? <Badge variant="outline">{isRoomContact(selectedContact) ? "群聊" : "好友"}</Badge> : null}
                    {selectedContact?.deleted ? <Badge variant="destructive">已删除</Badge> : null}
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    variant="outline"
                    onClick={() => void refreshMessages(selectedDevice, selectedWxid, selectedOwnerWxid)}
                    disabled={!selectedDevice || !selectedWxid || !adminPassword}
                  >
                    <RefreshCw className="h-4 w-4" />
                    刷新消息
                  </Button>
                  <Button onClick={() => openSendDialog()} disabled={!selectedDevice || !selectedWxid || !adminPassword || selectedModuleOffline}>
                    <Send className="h-4 w-4" />
                    发消息
                  </Button>
                </div>
              </CardHeader>
              <CardContent className="grid gap-3">
                <div className="rounded-lg border bg-secondary/35 p-3">
                  <div className="grid gap-2 text-xs text-muted-foreground md:grid-cols-3">
                    <span className="truncate">好友：{selectedContact ? contactName(selectedContact) : "-"}</span>
                    <span className="truncate">会话类型：{selectedContact ? contactKindText(selectedContact) : "-"}</span>
                    <span className="truncate">最近上报：{formatBeijingDateTime(selectedContact?.last_seen_at || selectedContact?.updated_at)}</span>
                  </div>
                </div>

                <div
                  ref={messageListRef}
                  onPointerDown={cancelPendingBottomScroll}
                  onScroll={cancelPendingBottomScroll}
                  onTouchStart={cancelPendingBottomScroll}
                  onWheel={cancelPendingBottomScroll}
                  className="grid h-[calc(100vh-492px)] min-h-[440px] content-start gap-3 overflow-y-auto overflow-x-hidden rounded-lg border bg-card p-3"
                >
                  {!selectedWxid ? (
                    <EmptyState icon={<MessageCircle className="h-5 w-5" />} text="请从好友列表选择一个对象" />
                  ) : messages.length === 0 ? (
                    <EmptyState icon={<MessageCircle className="h-5 w-5" />} text="当前好友暂无消息记录" />
                  ) : (
                    messages.map((item) => (
                      <MessageBubble
                        key={`${item.id}:${item.created_at}`}
                        message={item}
                        adminPassword={adminPassword}
                        contactByWxid={contactByWxid}
                        module={selectedModule}
                      />
                    ))
                  )}
                </div>

                <div className="grid gap-2 rounded-lg border bg-card p-3">
                  <Label htmlFor="inline-send">发送内容</Label>
                  <Textarea
                    id="inline-send"
                    value={draft}
                    onChange={(event) => setDraft(event.target.value)}
                    placeholder="输入要发送到微信的文本"
                    disabled={!selectedWxid || sending || selectedModuleOffline}
                  />
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="text-xs text-muted-foreground">
                      {selectedModuleOffline ? "当前微信离线，发送任务已清空" : pendingCount > 0 ? `待发送 ${pendingCount} 条` : failedCount > 0 ? `失败 ${failedCount} 条` : "发送队列空闲"}
                    </span>
                    <Button onClick={() => void submitSend()} disabled={sending || selectedModuleOffline || !adminPassword || !selectedDevice || !selectedWxid || !draft.trim()}>
                      <Send className="h-4 w-4" />
                      {sending ? "发送中" : "加入队列"}
                    </Button>
                  </div>
                </div>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <KeyRound className="h-4 w-4" />
                API Key 与设备管理
              </CardTitle>
              <Button variant="outline" onClick={() => void refreshApiKeys()} disabled={loading || !adminPassword}>
                <RefreshCw className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
                刷新 API Key
              </Button>
            </CardHeader>
            <CardContent className="grid gap-4 xl:grid-cols-[minmax(0,360px)_minmax(0,360px)_minmax(0,1fr)]">
              <div className="grid content-start gap-3 rounded-lg border bg-card p-3">
                <div className="text-sm font-medium">生成 API Key</div>
                <div className="grid gap-2">
                  <Label htmlFor="new-api-key">指定 Key</Label>
                  <Input
                    id="new-api-key"
                    value={newApiKey}
                    onChange={(event) => setNewApiKey(event.target.value)}
                    placeholder="留空则服务端自动生成"
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="new-api-key-device">设备名</Label>
                  <Input
                    id="new-api-key-device"
                    value={newApiKeyDevice}
                    onChange={(event) => setNewApiKeyDevice(event.target.value)}
                    placeholder="留空则服务端自动生成"
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="new-api-key-nickname">显示名</Label>
                  <Input
                    id="new-api-key-nickname"
                    value={newApiKeyNickname}
                    onChange={(event) => setNewApiKeyNickname(event.target.value)}
                    placeholder="Web 端显示名称"
                  />
                </div>
                <Button onClick={() => void submitApiKey()} disabled={savingAdmin || !adminPassword}>
                  <KeyRound className="h-4 w-4" />
                  {savingAdmin ? "生成中" : "生成 API Key"}
                </Button>
              </div>

              <div className="grid content-start gap-3 rounded-lg border bg-card p-3">
                <div className="text-sm font-medium">当前设备</div>
                <div className="grid gap-1 text-sm">
                  <span className="text-muted-foreground">设备名</span>
                  <span className="font-medium">{selectedDevice || "-"}</span>
                </div>
                <div className="grid gap-1 text-sm">
                  <span className="text-muted-foreground">当前 wxid</span>
                  <span className="break-all font-mono text-xs">{selectedOwnerWxid || "-"}</span>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="device-nickname">Web 显示名</Label>
                  <Input
                    id="device-nickname"
                    value={deviceNicknameDraft}
                    onChange={(event) => setDeviceNicknameDraft(event.target.value)}
                    placeholder="只在 Web 端设置"
                    disabled={!selectedDevice}
                  />
                </div>
                <Button onClick={() => void submitDeviceNickname()} disabled={savingAdmin || !adminPassword || !selectedDevice}>
                  <Smartphone className="h-4 w-4" />
                  保存设备显示名
                </Button>
              </div>

              <div className="overflow-auto rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="min-w-[220px]">API Key</TableHead>
                      <TableHead className="w-[96px]">状态</TableHead>
                      <TableHead className="w-[128px]">操作</TableHead>
                      <TableHead className="min-w-[150px]">设备名</TableHead>
                      <TableHead className="min-w-[160px]">显示名</TableHead>
                      <TableHead className="min-w-[180px]">更新时间</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {apiKeys.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={6}>
                          <EmptyState icon={<KeyRound className="h-5 w-5" />} text="暂无 API Key，请先连接刷新或生成" />
                        </TableCell>
                      </TableRow>
                    ) : (
                      apiKeys.slice(0, 12).map((item) => (
                        <TableRow key={item.code}>
                          <TableCell className="font-mono text-xs">{item.api_key || item.code}</TableCell>
                          <TableCell className="text-xs">
                            <Badge variant={item.enabled === false ? "secondary" : "success"}>
                              {item.enabled === false ? "停用" : "启用"}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <div className="flex items-center gap-1">
                              <Button
                                variant="ghost"
                                size="icon"
                                title={item.enabled === false ? "启用 API Key" : "停用 API Key"}
                                onClick={() => void toggleApiKeyEnabled(item)}
                                disabled={savingAdmin || !adminPassword}
                              >
                                {item.enabled === false ? <PlayCircle className="h-4 w-4" /> : <PauseCircle className="h-4 w-4" />}
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon"
                                title="删除 API Key"
                                onClick={() => void revokeApiKey(item.api_key || item.code)}
                                disabled={savingAdmin || !adminPassword}
                              >
                                <Trash2 className="h-4 w-4" />
                              </Button>
                            </div>
                          </TableCell>
                          <TableCell className="text-xs">{item.device || "自动生成"}</TableCell>
                          <TableCell className="text-xs">{item.nickname || "-"}</TableCell>
                          <TableCell className="text-xs">{formatBeijingTimeAgo(item.updated_at || item.created_at)}</TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Activity className="h-4 w-4" />
                模块运行明细
              </CardTitle>
              <Button variant="outline" onClick={() => void refreshModules()} disabled={loading || !adminPassword}>
                <RefreshCw className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
                刷新
              </Button>
            </CardHeader>
            <CardContent>
              <div className="overflow-auto rounded-md border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="min-w-[160px]">设备</TableHead>
                      <TableHead className="min-w-[120px]">状态</TableHead>
                      <TableHead className="min-w-[140px]">队列</TableHead>
                      <TableHead className="min-w-[220px]">最近活动</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {modules.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={4}>
                          <EmptyState icon={<Smartphone className="h-5 w-5" />} text="未读取到模块状态" />
                        </TableCell>
                      </TableRow>
                    ) : (
                      modules.map((item) => (
                        <TableRow key={item.device} className={item.device === selectedDevice ? "bg-secondary/70" : undefined}>
                          <TableCell>
                            <div className="font-medium">{item.device}</div>
                            <div className="truncate text-xs text-muted-foreground">{item.device_nickname || "未上报微信信息"}</div>
                          </TableCell>
                          <TableCell>
                            <StatusBadge status={item.runtime_status} />
                          </TableCell>
                          <TableCell>
                            <div className="flex flex-wrap gap-1 text-xs">
                              <Badge variant="warning">待 {item.pending_outbox ?? 0}</Badge>
                              <Badge variant="secondary">中 {item.leased_outbox ?? 0}</Badge>
                              <Badge variant={item.failed_outbox ? "destructive" : "success"}>败 {item.failed_outbox ?? 0}</Badge>
                            </div>
                          </TableCell>
                          <TableCell className="text-xs">
                            <div>拉取：{formatBeijingDateTime(item.last_poll_at)}</div>
                            <div className="text-muted-foreground">回执：{formatBeijingDateTime(item.last_ack_at || item.last_outbound_ack_at)}</div>
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </div>
            </CardContent>
          </Card>
        </section>
      </main>

      <Dialog open={sendOpen} onOpenChange={setSendOpen}>
        <DialogContent onClose={() => setSendOpen(false)}>
          <DialogHeader>
            <DialogTitle>发送微信消息</DialogTitle>
            <DialogDescription>{selectedChatTitle(selectedWxid, selectedContact)}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-2">
              <Label htmlFor="send-target">发送对象</Label>
              <Input id="send-target" value={selectedChatTitle(selectedWxid, selectedContact)} readOnly />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="send-text">消息内容</Label>
              <Textarea
                id="send-text"
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                placeholder="输入要发送到微信的文本"
                autoFocus
                disabled={selectedModuleOffline}
              />
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setSendOpen(false)}>
              取消
            </Button>
            <Button type="button" onClick={() => void submitSend()} disabled={sending || selectedModuleOffline || !draft.trim()}>
              <Send className="h-4 w-4" />
              {sending ? "发送中" : "加入队列"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function MessageBubble({
  message,
  adminPassword,
  contactByWxid,
  module
}: {
  message: StoredMessage;
  adminPassword: string;
  contactByWxid: Map<string, ModuleContact>;
  module?: ModuleStatus;
}) {
  const outgoing = message.direction === "sent";
  const hasAttachment = Boolean(message.media_url || message.media_kind || mediaKindFromType(message.message_type));
  const senderName = messageSenderName(message, contactByWxid, module);
  return (
    <div className={outgoing ? "flex justify-end" : "flex justify-start"}>
      <div
        className={
          outgoing
            ? "max-w-[82%] rounded-lg border border-primary/20 bg-primary px-3 py-2 text-primary-foreground shadow-sm"
            : "max-w-[82%] rounded-lg border bg-secondary px-3 py-2 text-secondary-foreground shadow-sm"
        }
      >
        <div className="mb-1 flex flex-wrap items-center gap-2 text-xs opacity-80">
          <span>{outgoing ? "发出" : "收到"}</span>
          <span className="font-medium">{senderName}</span>
          {message.raw_provider ? <span>{providerText(message.raw_provider)}</span> : null}
          {message.message_type ? <span>{messageTypeText(message.message_type)}</span> : null}
          <span>{formatBeijingDateTime(message.created_at)}</span>
        </div>
        {message.text ? <div className="whitespace-pre-wrap break-words text-sm leading-6">{message.text}</div> : null}
        {hasAttachment ? <MessageAttachment message={message} adminPassword={adminPassword} outgoing={outgoing} /> : null}
      </div>
    </div>
  );
}

function MessageAttachment({
  message,
  adminPassword,
  outgoing
}: {
  message: StoredMessage;
  adminPassword: string;
  outgoing: boolean;
}) {
  const kind = message.media_kind || mediaKindFromType(message.message_type) || "file";
  const url = message.media_url ? mediaURL(message.media_url, adminPassword) : "";
  const title = message.media_name || messageTypeText(message.message_type);
  const shell = outgoing ? "border-primary-foreground/30 bg-primary-foreground/10" : "border-border bg-background/80";

  if (!url) {
    return (
      <div className={`mt-2 rounded-md border border-dashed p-3 text-xs ${shell}`}>
        <div className="flex items-center gap-2 font-medium">
          {mediaIcon(kind)}
          <span>{title}</span>
        </div>
        <div className="mt-1 opacity-80">模块已收到此类消息，但附件文件还没有上传。</div>
      </div>
    );
  }

  if (kind === "image") {
    return (
      <a href={url} target="_blank" rel="noreferrer" className={`mt-2 block overflow-hidden rounded-md border ${shell}`}>
        <img src={url} alt={title} className="max-h-72 w-full object-contain" />
      </a>
    );
  }

  if (kind === "voice") {
    return (
      <div className={`mt-2 grid gap-2 rounded-md border p-3 ${shell}`}>
        <div className="flex items-center gap-2 text-sm font-medium">
          <Mic className="h-4 w-4" />
          <span>{title}</span>
        </div>
        <audio controls preload="none" src={url} className="h-9 w-full" />
        <MediaDownload url={url} name={title} size={message.media_size} />
      </div>
    );
  }

  if (kind === "video") {
    return (
      <div className={`mt-2 overflow-hidden rounded-md border ${shell}`}>
        <video controls preload="metadata" src={url} className="max-h-80 w-full bg-black" />
      </div>
    );
  }

  return (
    <div className={`mt-2 grid gap-2 rounded-md border p-3 text-sm ${shell}`}>
      <div className="flex items-center gap-2 font-medium">
        {mediaIcon(kind)}
        <span className="truncate">{title}</span>
      </div>
      <MediaDownload url={url} name={title} size={message.media_size} />
    </div>
  );
}

function MediaDownload({ url, name, size }: { url: string; name: string; size?: number }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      download={name}
      className="inline-flex h-8 w-fit items-center gap-2 rounded-md border bg-background px-3 text-xs text-foreground shadow-sm hover:bg-secondary"
    >
      <Download className="h-3.5 w-3.5" />
      下载{size ? ` · ${formatBytes(size)}` : ""}
    </a>
  );
}

function ConnectionBadge({ connected, enabled }: { connected: boolean; enabled: boolean }) {
  if (!enabled) {
    return (
      <Badge variant="secondary">
        <WifiOff className="mr-1 h-3.5 w-3.5" />
        实时关闭
      </Badge>
    );
  }
  return (
    <Badge variant={connected ? "success" : "warning"}>
      {connected ? <Wifi className="mr-1 h-3.5 w-3.5" /> : <WifiOff className="mr-1 h-3.5 w-3.5" />}
      {connected ? "实时已连接" : "实时待连接"}
    </Badge>
  );
}

function StatusBadge({ status }: { status?: string }) {
  if (status === "ready") {
    return <Badge variant="success">就绪</Badge>;
  }
  if (status === "pending" || status === "sending") {
    return <Badge variant="warning">{statusText(status)}</Badge>;
  }
  if (status === "failed" || status === "unregistered") {
    return <Badge variant="destructive">{statusText(status)}</Badge>;
  }
  if (status === "offline") {
	return <Badge variant="destructive"><WifiOff className="mr-1 h-3.5 w-3.5" />离线</Badge>;
  }
  if (status === "disabled") {
    return <Badge variant="secondary">{statusText(status)}</Badge>;
  }
  return <Badge variant="secondary">{statusText(status)}</Badge>;
}

function MetricCard({ title, value, icon }: { title: string; value: string; icon: React.ReactNode }) {
  return (
    <Card>
      <CardContent className="flex min-h-[88px] items-center justify-between p-4">
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground">{title}</div>
          <div className="mt-1 truncate text-xl font-semibold">{value}</div>
        </div>
        <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-secondary text-muted-foreground">
          {icon}
        </div>
      </CardContent>
    </Card>
  );
}

function SignalPill({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border bg-background px-2 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-1 truncate text-xs font-medium">{value}</div>
    </div>
  );
}

function EmptyState({ icon, text }: { icon: React.ReactNode; text: string }) {
  return (
    <div className="grid min-h-[120px] place-items-center rounded-lg border border-dashed bg-background/60 p-6 text-center text-sm text-muted-foreground">
      <div className="grid justify-items-center gap-2">
        <div className="text-muted-foreground">{icon}</div>
        <div>{text}</div>
      </div>
    </div>
  );
}

function contactName(contact: ModuleContact) {
  if (isFileHelperContact(contact)) {
    return contact.remark || contact.nickname || "文件传输助手";
  }
  const name = contact.remark || contact.nickname || contact.alias;
  if (name) {
    return name;
  }
  return isRoomContact(contact) ? "未命名群聊" : "未命名好友";
}

function contactSecondary(contact: ModuleContact) {
  const nickname = contact.nickname && contact.nickname !== contactName(contact) ? contact.nickname : "";
  const parts = [nickname, contact.alias].filter(Boolean);
  return parts.join(" · ") || "-";
}

function contactKindText(contact: ModuleContact) {
  return isRoomContact(contact) ? "群聊" : "好友";
}

function selectedChatTitle(wxid: string, contact?: ModuleContact) {
  if (contact) {
    return contactName(contact);
  }
  if (!wxid) {
    return "未选择好友";
  }
  return wxid.toLowerCase().endsWith("@chatroom") ? "未收录群聊" : "未收录好友";
}

function messageChatId(message: StoredMessage) {
  if (message.chat_id) {
    return message.chat_id;
  }
  if (message.room_id) {
    return message.room_id;
  }
  if (message.direction === "sent") {
    return message.to_wxid || message.from_wxid || message.sender_wxid || "";
  }
  return message.from_wxid || message.sender_wxid || message.to_wxid || "";
}

function messageIsRoom(message: StoredMessage, contact?: ModuleContact) {
  const chatId = messageChatId(message).toLowerCase();
  return Boolean(contact?.chatroom || message.chat_kind === "room" || message.room_id || chatId.endsWith("@chatroom"));
}

function messageContactName(message: StoredMessage, contact?: ModuleContact) {
  if (contact) {
    return contactName(contact);
  }
  return messageIsRoom(message) ? "未收录群聊" : "未收录好友";
}

function messageSenderName(message: StoredMessage, contactByWxid: Map<string, ModuleContact>, module?: ModuleStatus) {
  const outgoing = message.direction === "sent";
  if (outgoing) {
    return moduleSelfName(module);
  }

  const senderWxid = message.sender_wxid || "";
  if (messageIsRoom(message) && !senderWxid) {
    return "群聊系统消息";
  }

  const wxid = senderWxid || message.from_wxid || "";
  const contact = wxid ? contactByWxid.get(wxid) : undefined;
  if (contact) {
    return contactName(contact);
  }

  if (messageIsRoom(message)) {
    return senderWxid ? "群成员" : "群聊";
  }
  return wxid && wxid === moduleOwnerWxid(module) ? moduleSelfName(module) : "对方";
}

function moduleSelfName(module?: ModuleStatus) {
  return module?.device_nickname || "我";
}

function messagePreview(message: StoredMessage, contactByWxid?: Map<string, ModuleContact>, module?: ModuleStatus) {
  const direction = message.direction === "sent" ? "发出" : "收到";
  const body = message.text?.trim() || message.media_name || messageTypeText(message.message_type);
  const sender = contactByWxid ? messageSenderName(message, contactByWxid, module) : "";
  return [direction, sender, body || "空消息"].filter(Boolean).join(" · ");
}

function isRoomContact(contact: ModuleContact) {
  return Boolean(contact.chatroom || contact.wxid.toLowerCase().endsWith("@chatroom"));
}

function isFileHelperContact(contact: ModuleContact) {
  return contact.wxid.toLowerCase() === "filehelper";
}

function statusText(status?: string) {
  switch (status) {
    case "ready":
      return "就绪";
    case "pending":
      return "待发送";
    case "sending":
      return "发送中";
    case "failed":
      return "失败";
	case "offline":
	  return "离线";
    case "disabled":
      return "已停用";
    case "unregistered":
      return "未注册";
    default:
      return status || "-";
  }
}

function providerText(provider: string) {
  switch (provider) {
    case RAW_PROVIDER_MODULE_ACK:
      return "模块回执";
    case "lsposed":
      return "LSPosed";
    case "module":
      return "模块";
    default:
      return provider;
  }
}

function messageTypeText(type: number) {
  switch (type) {
    case 1:
      return "文本";
    case 3:
      return "图片";
    case 34:
      return "语音";
    case 43:
      return "视频";
    case 47:
      return "表情";
    case 48:
      return "位置";
    case 49:
      return "链接/文件";
    case 62:
      return "小视频";
    case 10000:
      return "系统消息";
    default:
      return `类型 ${type}`;
  }
}

function mediaKindFromType(type?: number) {
  switch (type) {
    case 3:
      return "image";
    case 34:
      return "voice";
    case 43:
    case 62:
      return "video";
    case 47:
      return "emoji";
    case 48:
      return "location";
    case 49:
      return "file";
    default:
      return "";
  }
}

function mediaURL(path: string, password: string) {
  const url = new URL(path.startsWith("/") ? publicPath(path) : path, window.location.origin);
  if (password) {
    url.searchParams.set("password", password);
  }
  return `${url.pathname}${url.search}`;
}

function mediaIcon(kind: string) {
  switch (kind) {
    case "image":
      return <ImageIcon className="h-4 w-4" />;
    case "voice":
      return <Mic className="h-4 w-4" />;
    case "video":
      return <Video className="h-4 w-4" />;
    case "file":
      return <Paperclip className="h-4 w-4" />;
    default:
      return <FileText className="h-4 w-4" />;
  }
}

function formatBytes(value?: number) {
  if (!value || value <= 0) {
    return "";
  }
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function liveEventTouchesChat(event: LiveMessageEvent, chatId: string) {
  return [event.chat_id, event.from, event.to, event.room_id, event.sender].some((value) => value === chatId);
}

function moduleOwnerWxid(module?: ModuleStatus) {
  return module?.device_wxid || "";
}

function chatKindForContact(wxid: string, contact?: ModuleContact) {
  if (contact?.chatroom || wxid.toLowerCase().endsWith("@chatroom")) {
    return "room";
  }
  return "direct";
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);

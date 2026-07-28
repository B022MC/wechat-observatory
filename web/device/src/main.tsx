import React from "react";
import { createRoot } from "react-dom/client";
import {
  CheckCircle2,
  KeyRound,
  Moon,
  Plus,
  RefreshCw,
  Save,
  ShieldCheck,
  Smartphone,
  Sun,
  Trash2,
  Wifi,
  WifiOff
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatBeijingDateTime, formatBeijingTimeAgo } from "@/time";
import {
  createDeviceApiKey,
  deleteDeviceApiKey,
  getDeviceApiKeys,
  getDeviceModules,
  setDeviceApiKeyEnabled,
  updateDeviceName
} from "./api";
import type { DeviceApiKey, DeviceModule } from "./types";
import "../../admin/src/index.css";

const PASSWORD_KEY = "wgc_device_admin_password";
const THEME_KEY = "wgc_admin_theme";

type ThemeMode = "light" | "dark";

function resolveTheme(): ThemeMode {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored === "light" || stored === "dark") return stored;
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function App() {
  const [password, setPassword] = React.useState(() => localStorage.getItem(PASSWORD_KEY) || "");
  const [theme, setTheme] = React.useState<ThemeMode>(() => resolveTheme());
  const [modules, setModules] = React.useState<DeviceModule[]>([]);
  const [apiKeys, setApiKeys] = React.useState<DeviceApiKey[]>([]);
  const [selectedDevice, setSelectedDevice] = React.useState("");
  const [nickname, setNickname] = React.useState("");
  const [newKey, setNewKey] = React.useState("");
  const [newNickname, setNewNickname] = React.useState("");
  const [newDevice, setNewDevice] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [notice, setNotice] = React.useState("输入设备管理密码后连接");

  const credential = password.trim();
  const selectedModule = modules.find((item) => item.device === selectedDevice);
  const onlineCount = modules.filter((item) => item.runtime_status === "online").length;
  const enabledKeyCount = apiKeys.filter((item) => item.enabled !== false).length;

  React.useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem(THEME_KEY, theme);
  }, [theme]);

  React.useEffect(() => {
    setNickname(selectedModule?.device_nickname || selectedModule?.device || "");
    setNewDevice(selectedModule?.device || "");
  }, [selectedModule?.device, selectedModule?.device_nickname]);

  const refresh = React.useCallback(async (quiet = false) => {
    if (!credential) {
      setModules([]);
      setApiKeys([]);
      return;
    }
    if (!quiet) setBusy(true);
    try {
      const [modulePayload, keyPayload] = await Promise.all([
        getDeviceModules(credential),
        getDeviceApiKeys(credential)
      ]);
      const nextModules = modulePayload.modules || [];
      setModules(nextModules);
      setApiKeys(keyPayload.api_keys || []);
      setSelectedDevice((current) => nextModules.some((item) => item.device === current)
        ? current
        : nextModules[0]?.device || "");
      localStorage.setItem(PASSWORD_KEY, credential);
      if (!quiet) setNotice("设备数据已刷新");
    } catch (error) {
      setModules([]);
      setApiKeys([]);
      setNotice(error instanceof Error ? error.message : "连接失败");
    } finally {
      if (!quiet) setBusy(false);
    }
  }, [credential]);

  React.useEffect(() => {
    if (!credential) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(true), 15000);
    return () => window.clearInterval(timer);
  }, [credential, refresh]);

  const saveDeviceName = async () => {
    if (!credential || !selectedDevice) return;
    setBusy(true);
    try {
      await updateDeviceName({ password: credential, name: selectedDevice, nickname: nickname.trim() });
      setNotice("设备名称已保存");
      await refresh(true);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "保存失败");
    } finally {
      setBusy(false);
    }
  };

  const createKey = async () => {
    if (!credential || !newDevice.trim()) return;
    setBusy(true);
    try {
      const payload = await createDeviceApiKey({
        password: credential,
        apiKey: newKey.trim() || undefined,
        device: newDevice.trim(),
        nickname: newNickname.trim() || undefined
      });
      setNewKey("");
      setNewNickname("");
      setNotice(`API Key 已创建：${payload.api_key.api_key || payload.api_key.code}`);
      await refresh(true);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "创建失败");
    } finally {
      setBusy(false);
    }
  };

  const toggleKey = async (item: DeviceApiKey) => {
    const apiKey = item.api_key || item.code;
    if (!credential || !apiKey) return;
    setBusy(true);
    try {
      await setDeviceApiKeyEnabled({ password: credential, apiKey, enabled: item.enabled === false });
      setNotice(item.enabled === false ? "API Key 已启用" : "API Key 已停用");
      await refresh(true);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "更新失败");
    } finally {
      setBusy(false);
    }
  };

  const removeKey = async (item: DeviceApiKey) => {
    const apiKey = item.api_key || item.code;
    if (!credential || !apiKey || !window.confirm(`确认删除 ${item.nickname || item.device || item.code}？`)) return;
    setBusy(true);
    try {
      await deleteDeviceApiKey({ password: credential, apiKey });
      setNotice("API Key 已删除");
      await refresh(true);
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "删除失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="sticky top-0 z-20 border-b bg-background/95 backdrop-blur">
        <div className="mx-auto flex max-w-[1500px] items-center justify-between gap-4 px-4 py-3 lg:px-6">
          <div className="flex items-center gap-3">
            <div className="grid h-10 w-10 place-items-center rounded-lg bg-primary text-primary-foreground">
              <Smartphone className="h-5 w-5" />
            </div>
            <div>
              <h1 className="font-semibold">设备管理</h1>
              <p className="text-xs text-muted-foreground">WeChat Observatory</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="icon" onClick={() => setTheme(theme === "dark" ? "light" : "dark")} aria-label="切换主题">
              {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            </Button>
            <Button variant="outline" onClick={() => void refresh()} disabled={!credential || busy}>
              <RefreshCw className={`h-4 w-4 ${busy ? "animate-spin" : ""}`} />
              刷新
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto grid max-w-[1500px] gap-4 p-4 lg:grid-cols-[280px_minmax(0,1fr)] lg:p-6">
        <aside className="grid content-start gap-4">
          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2 text-base"><ShieldCheck className="h-4 w-4" />安全连接</CardTitle></CardHeader>
            <CardContent className="grid gap-3">
              <Label htmlFor="device-password">设备管理密码</Label>
              <Input id="device-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} onKeyDown={(event) => event.key === "Enter" && void refresh()} />
              <Button onClick={() => void refresh()} disabled={!credential || busy}>连接</Button>
              <p className="break-words text-xs text-muted-foreground">{notice}</p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader><CardTitle className="text-base">设备</CardTitle></CardHeader>
            <CardContent className="grid gap-2">
              {modules.length === 0 ? <p className="text-sm text-muted-foreground">暂无设备数据</p> : modules.map((item) => (
                <button key={item.device} onClick={() => setSelectedDevice(item.device)} className={`flex items-center justify-between rounded-md border px-3 py-3 text-left transition-colors ${selectedDevice === item.device ? "border-primary bg-secondary" : "hover:bg-secondary/60"}`}>
                  <span className="min-w-0">
                    <strong className="block truncate text-sm">{item.device_nickname || item.device}</strong>
                    <small className="block truncate text-muted-foreground">{item.device}</small>
                  </span>
                  <StatusBadge status={item.runtime_status} />
                </button>
              ))}
            </CardContent>
          </Card>
        </aside>

        <section className="grid min-w-0 content-start gap-4">
          <div className="grid gap-3 sm:grid-cols-3">
            <Metric title="设备总数" value={String(modules.length)} icon={<Smartphone className="h-4 w-4" />} />
            <Metric title="在线设备" value={String(onlineCount)} icon={<Wifi className="h-4 w-4" />} />
            <Metric title="已启用 Key" value={String(enabledKeyCount)} icon={<KeyRound className="h-4 w-4" />} />
          </div>

          <Card>
            <CardHeader><CardTitle className="text-base">当前设备</CardTitle></CardHeader>
            <CardContent className="grid gap-4 md:grid-cols-2">
              <div className="grid gap-3 rounded-lg border bg-secondary/30 p-4 text-sm">
                <Detail label="设备标识" value={selectedModule?.device || "-"} />
                <Detail label="当前微信" value={selectedModule?.device_wxid || "未注册"} />
                <Detail label="连接状态" value={statusText(selectedModule?.runtime_status)} />
                <Detail label="最近活动" value={selectedModule?.last_seen_at ? `${formatBeijingTimeAgo(selectedModule.last_seen_at)} · ${formatBeijingDateTime(selectedModule.last_seen_at)}` : "-"} />
              </div>
              <div className="grid content-start gap-3">
                <Label htmlFor="device-nickname">设备显示名</Label>
                <Input id="device-nickname" value={nickname} onChange={(event) => setNickname(event.target.value)} disabled={!selectedDevice} />
                <Button className="w-fit" onClick={() => void saveDeviceName()} disabled={!selectedDevice || busy}>
                  <Save className="h-4 w-4" />保存名称
                </Button>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2 text-base"><Plus className="h-4 w-4" />创建 API Key</CardTitle></CardHeader>
            <CardContent className="grid gap-3 md:grid-cols-4">
              <div className="grid gap-2"><Label>设备</Label><Input value={newDevice} onChange={(event) => setNewDevice(event.target.value)} placeholder="设备标识" /></div>
              <div className="grid gap-2"><Label>名称</Label><Input value={newNickname} onChange={(event) => setNewNickname(event.target.value)} placeholder="用途说明" /></div>
              <div className="grid gap-2"><Label>自定义 Key（可选）</Label><Input value={newKey} onChange={(event) => setNewKey(event.target.value)} placeholder="留空自动生成" /></div>
              <div className="flex items-end"><Button className="w-full" onClick={() => void createKey()} disabled={!credential || !newDevice.trim() || busy}><Plus className="h-4 w-4" />创建</Button></div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2 text-base"><KeyRound className="h-4 w-4" />API Key</CardTitle></CardHeader>
            <CardContent className="p-0">
              <div className="grid gap-3 p-4 md:hidden">
                {apiKeys.length === 0 ? <p className="py-6 text-center text-sm text-muted-foreground">暂无 API Key</p> : apiKeys.map((item) => (
                  <div key={item.code || item.api_key} className="grid gap-3 rounded-lg border p-4">
                    <div className="flex items-start justify-between gap-3"><div><strong className="text-sm">{item.nickname || "未命名"}</strong><p className="text-xs text-muted-foreground">{item.device || "未绑定"}</p></div><Badge variant={item.enabled === false ? "secondary" : "success"}>{item.enabled === false ? "已停用" : "已启用"}</Badge></div>
                    <div className="break-all rounded-md bg-secondary px-3 py-2 font-mono text-xs">{item.api_key || item.code}</div>
                    <div className="flex items-center justify-between gap-3"><span className="text-xs text-muted-foreground">{item.updated_at ? formatBeijingDateTime(item.updated_at) : "-"}</span><div className="flex gap-2"><Button size="xs" variant="outline" onClick={() => void toggleKey(item)} disabled={busy}>{item.enabled === false ? "启用" : "停用"}</Button><Button size="icon" variant="ghost" onClick={() => void removeKey(item)} disabled={busy} aria-label="删除"><Trash2 className="h-4 w-4 text-destructive" /></Button></div></div>
                  </div>
                ))}
              </div>
              <div className="hidden overflow-x-auto md:block">
                <Table>
                  <TableHeader><TableRow><TableHead>名称</TableHead><TableHead>设备</TableHead><TableHead>Key</TableHead><TableHead>更新时间</TableHead><TableHead>状态</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader>
                  <TableBody>
                    {apiKeys.length === 0 ? <TableRow><TableCell colSpan={6} className="py-10 text-center text-muted-foreground">暂无 API Key</TableCell></TableRow> : apiKeys.map((item) => (
                      <TableRow key={item.code || item.api_key}>
                        <TableCell className="font-medium">{item.nickname || "未命名"}</TableCell>
                        <TableCell>{item.device || "未绑定"}</TableCell>
                        <TableCell className="max-w-[260px] truncate font-mono text-xs">{item.api_key || item.code}</TableCell>
                        <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{item.updated_at ? formatBeijingDateTime(item.updated_at) : "-"}</TableCell>
                        <TableCell><Badge variant={item.enabled === false ? "secondary" : "success"}>{item.enabled === false ? "已停用" : "已启用"}</Badge></TableCell>
                        <TableCell><div className="flex justify-end gap-2"><Button size="xs" variant="outline" onClick={() => void toggleKey(item)} disabled={busy}>{item.enabled === false ? "启用" : "停用"}</Button><Button size="icon" variant="ghost" onClick={() => void removeKey(item)} disabled={busy} aria-label="删除"><Trash2 className="h-4 w-4 text-destructive" /></Button></div></TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </CardContent>
          </Card>
        </section>
      </main>
    </div>
  );
}

function StatusBadge({ status }: { status?: string }) {
  if (status === "online") return <Badge variant="success"><CheckCircle2 className="mr-1 h-3 w-3" />在线</Badge>;
  if (status === "disabled") return <Badge variant="secondary">停用</Badge>;
  return <Badge variant="warning"><WifiOff className="mr-1 h-3 w-3" />未注册</Badge>;
}

function Metric({ title, value, icon }: { title: string; value: string; icon: React.ReactNode }) {
  return <Card><CardContent className="flex min-h-[88px] items-center justify-between p-4"><div><p className="text-xs text-muted-foreground">{title}</p><strong className="mt-1 block text-2xl">{value}</strong></div><div className="grid h-10 w-10 place-items-center rounded-md bg-secondary text-muted-foreground">{icon}</div></CardContent></Card>;
}

function Detail({ label, value }: { label: string; value: string }) {
  return <div className="flex items-start justify-between gap-4"><span className="text-muted-foreground">{label}</span><strong className="break-all text-right font-medium">{value}</strong></div>;
}

function statusText(status?: string) {
  if (status === "online") return "在线";
  if (status === "disabled") return "已停用";
  if (status === "unregistered") return "未注册";
  return status || "-";
}

createRoot(document.getElementById("root")!).render(<React.StrictMode><App /></React.StrictMode>);

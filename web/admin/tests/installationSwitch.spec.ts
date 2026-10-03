import { test, expect } from "@playwright/test";

const HINT = "同一 Key 可在多台手机间切换：在哪台手机打开微信，哪台就接管；在用手机离线约 2 分钟后，在线的待命手机自动接管。";

test("module rows list the binding's phones and request a switch to a standby phone", async ({ page }) => {
  const device = "phone/1";
  const switchCalls: { path: string; body: unknown }[] = [];
  let switchReply: { status: number; json: unknown } = { status: 200, json: {} };
  let pending = false;
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("dialog", (dialog) => void dialog.accept());
  await page.addInitScript(() => {
    localStorage.setItem("wgc_admin_password", "test");
    class Events extends EventTarget {
      close() {}
    }
    Object.assign(window, { EventSource: Events });
  });
  const now = new Date().toISOString();
  const expiresAt = new Date(Date.now() + 10 * 60_000).toISOString();
  const installations = () => [
    {
      id: 11, short_id: "aaaa1111", active: true, state: "active", owner_wxid: "wxid_a", wechat_nickname: "小明",
      device_model: "Xiaomi 2312DRAABC", android_version: "14", wechat_version: "8.0.50", module_version: "0.1.12",
      last_seen_at: now, last_active_at: now, switch_pending: false
    },
    { id: 22, short_id: "bbbb2222", active: false, state: "standby", owner_wxid: "wxid_b", last_seen_at: now, switch_pending: pending },
    { id: 33, short_id: "cccc3333", active: false, state: "offline", device_model: "Pixel 7", switch_pending: false }
  ];
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() === "POST" && url.pathname.endsWith("/switch")) {
      switchCalls.push({ path: url.pathname, body: route.request().postDataJSON() });
      if (switchReply.status === 200) {
        pending = true;
        return route.fulfill({ json: { ok: true, switch_request: { installation_id: 22, expires_at: expiresAt } } });
      }
      return route.fulfill({ status: switchReply.status, json: switchReply.json });
    }
    if (url.pathname.endsWith("/modules/status")) {
      return route.fulfill({
        json: {
          modules: [{
            device, device_wxid: "wxid_a", account_generation: 1, runtime_status: "ready",
            installations: installations(),
            ...(pending ? { switch_request: { installation_id: 22, expires_at: expiresAt } } : {})
          }]
        }
      });
    }
    if (url.pathname.endsWith("/api-keys")) return route.fulfill({ json: { api_keys: [] } });
    if (url.pathname.endsWith("/module-contacts")) return route.fulfill({ json: { contacts: [] } });
    if (url.pathname.endsWith("/messages")) return route.fulfill({ json: { messages: [] } });
    return route.fulfill({ status: 404, json: {} });
  });

  await page.goto("/");
  await page.getByRole("button", { name: "连接刷新", exact: true }).click();

  const list = page.getByTestId("installation-list").first();
  const rows = list.getByTestId("installation-row");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0).getByText("在用", { exact: true })).toBeVisible();
  await expect(rows.nth(0).getByText("Xiaomi 2312DRAABC", { exact: true })).toBeVisible();
  await expect(rows.nth(0).getByText("Android 14 · 微信 8.0.50 · 模块 0.1.12", { exact: true })).toBeVisible();
  await expect(rows.nth(0).getByText("小明（wxid_a）")).toBeVisible();
  await expect(rows.nth(0).getByRole("button", { name: "切到这台手机" })).toHaveCount(0);
  await expect(rows.nth(1).getByText("待命", { exact: true })).toBeVisible();
  await expect(rows.nth(1).getByText("旧版模块", { exact: true })).toBeVisible();
  await expect(rows.nth(1).getByText("#bbbb2222", { exact: true })).toBeVisible();
  await expect(rows.nth(2).getByText("离线", { exact: true })).toBeVisible();
  await expect(page.getByText(HINT, { exact: true })).toBeVisible();
  // The detail table shows the same phones in compact form, without switch buttons.
  await expect(page.getByTestId("installation-list")).toHaveCount(2);
  await expect(page.getByTestId("installation-list").nth(1).getByRole("button")).toHaveCount(0);

  await rows.nth(1).getByRole("button", { name: "切到这台手机" }).click();
  await expect.poll(() => switchCalls.length).toBe(1);
  expect(switchCalls[0]).toEqual({ path: "/api/modules/phone%2F1/switch", body: { installation_id: 22 } });
  await expect(list.getByText("已请求切换，目标手机下一次心跳（约 10 秒内）接管；10 分钟内未上线则作废", { exact: true })).toBeVisible();
  await expect(rows.nth(1).getByText("切换中", { exact: true })).toBeVisible();
  await expect(list.getByText(/^切换请求有效至/)).toBeVisible();

  switchReply = { status: 404, json: { ok: false, code: "installation_not_found", message: "installation not found" } };
  await rows.nth(2).getByRole("button", { name: "切到这台手机" }).click();
  await expect(list.getByText("installation not found", { exact: true })).toBeVisible();

  switchReply = { status: 409, json: { ok: false, code: "installation_active", message: "installation already active" } };
  await rows.nth(2).getByRole("button", { name: "切到这台手机" }).click();
  await expect(list.getByText("这台手机已是在用手机，列表已刷新", { exact: true })).toBeVisible();
  await expect(page.getByText("installation already active")).toHaveCount(0);
  expect(switchCalls).toHaveLength(3);
  expect(errors).toEqual([]);
});

test("rows from servers without installations hide the phone section", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("wgc_admin_password", "test");
    class Events extends EventTarget {
      close() {}
    }
    Object.assign(window, { EventSource: Events });
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/modules/status")) {
      return route.fulfill({ json: { modules: [{ device: "phone", device_wxid: "A", account_generation: 1, runtime_status: "ready" }] } });
    }
    if (url.pathname.endsWith("/api-keys")) return route.fulfill({ json: { api_keys: [] } });
    if (url.pathname.endsWith("/module-contacts")) return route.fulfill({ json: { contacts: [] } });
    if (url.pathname.endsWith("/messages")) return route.fulfill({ json: { messages: [] } });
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "连接刷新", exact: true }).click();
  await expect(page.getByText("待发：0")).toBeVisible();
  await expect(page.getByTestId("installation-list")).toHaveCount(0);
  await expect(page.getByText(HINT)).toHaveCount(0);
});

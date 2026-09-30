import { test, expect, type Route } from "@playwright/test";

test("switching accounts isolates shared contacts, delayed responses and drafts", async ({ page }) => {
  let owner = "A", generation = 1;
  let delayed: Route | undefined;
  let hold = false;
  const sent: unknown[] = [];
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript(() => {
    localStorage.setItem("wgc_admin_password", "test");
    class Events extends EventTarget {
      static all: Events[] = [];
      closed = false;
      constructor() { super(); Events.all.push(this); }
      close() { this.closed = true; }
    }
    Object.assign(window, { EventSource: Events, testEvents: Events });
  });
  const contact = (account: string, remark: string) => ({ id: 1, device: "phone", owner_wxid: account, wxid: "shared", remark, chatroom: false, deleted: false });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/modules/status")) {
      return route.fulfill({ json: { modules: [{ device: "phone", device_wxid: owner, account_generation: generation, runtime_status: "ready" }] } });
    }
    if (url.pathname.endsWith("/api-keys")) return route.fulfill({ json: { api_keys: [] } });
    if (url.pathname.endsWith("/module-contacts")) {
      if (hold && url.searchParams.get("owner_wxid") === "A") { hold = false; delayed = route; return; }
      const account = url.searchParams.get("owner_wxid") ?? "";
      return route.fulfill({ json: { contacts: [contact(account, `${account}${generation} remark`), contact(account === "A" ? "B" : "A", "foreign contact")] } });
    }
    if (url.pathname.endsWith("/messages")) {
      const account = url.searchParams.get("owner_wxid") ?? "";
      return route.fulfill({ json: { messages: [{ id: generation, device: "phone", owner_wxid: account, chat_id: "shared", direction: "recv", from_wxid: "shared", to_wxid: account, text: `${account}${generation} message`, message_type: 1 }] } });
    }
    if (url.pathname.endsWith("/send/text")) { sent.push(route.request().postDataJSON()); return route.fulfill({ json: { ok: true, outbox_id: 1 } }); }
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto("/");
  const refresh = page.getByRole("button", { name: "连接刷新", exact: true });
  await refresh.click();
  await expect(page.getByText("A1 remark", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("A1 message", { exact: true })).toBeVisible();
  await expect(page.getByText("foreign contact", { exact: true })).toHaveCount(0);
  const draft = page.getByPlaceholder("输入要发送到微信的文本").first();
  await draft.fill("draft belonging to A1");
  // Leave a contact request in flight across a full account round trip.
  hold = true;
  await page.getByPlaceholder("搜索昵称、备注、别名").fill("shared");
  await expect.poll(() => !!delayed).toBe(true);
  owner = "B"; generation = 2;
  await refresh.click();
  await expect(page.getByText("B2 remark", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("B2 message", { exact: true })).toBeVisible();
  await expect(draft).toHaveValue("");
  await draft.fill("draft belonging to B2");
  owner = "A"; generation = 3;
  await refresh.click();
  await expect(page.getByText("A3 remark", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("A3 message", { exact: true })).toBeVisible();
  await expect(draft).toHaveValue("");
  await delayed!.fulfill({ json: { contacts: [contact("A", "STALE A1 remark")] } });
  await page.waitForTimeout(100);
  await expect(page.getByText("STALE A1 remark", { exact: true })).toHaveCount(0);
  await expect(page.getByText("A3 remark", { exact: true }).first()).toBeVisible();
  await draft.fill("current A3 draft");
  // Same owner with a new generation means A -> B -> A happened between refreshes.
  generation = 5;
  await refresh.click();
  await expect(page.getByText("A5 remark", { exact: true }).first()).toBeVisible();
  await expect(draft).toHaveValue("");
  await draft.fill("current send");
  await page.getByRole("button", { name: "加入队列", exact: true }).last().click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0]).toMatchObject({ device: "phone", owner_wxid: "A", account_generation: 5, wx_ids: ["shared"], text: "current send" });
  expect(errors).toEqual([]);
});

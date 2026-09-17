# Android 模块的微信版本自适应方案

状态：**P1 已实现**（探针工具 + 模块启动自检），P2/P3 为设计。
关联代码：`android-module/`、`tools/wechat_hook_probe.py`

## 1. 问题

模块通过**反射调用微信内部类/字段**来收发消息。微信每个版本用新的随机种子重新混淆
（`-repackageclasses`），类名、字段名全部改变，因此：

- 写死名字 = 版本锁死；
- 版本一换，发送链路静默失效（观测还能用，回复发不出去）；
- 现有代码在 `findClass` 失败时静默 fallback，线上只能靠 adb 抓 logcat 排查。

实测（`tools/wechat_hook_probe.py`）：

| 微信版本 | versionCode | observation | identity | send | bootstrap |
|---|---|---|---|---|---|
| 8.0.74 | 3120 | ok | ok | ok（现网验证） | ok |
| 8.0.76 | 3141 | ok | ok | **missing 14/18** | **missing 6/11** |
| 8.0.78 | 3180 | ok | ok | **missing 15/18** | **missing 6/11** |

另外 8.0.78 里连调用**形状**都变了：旧签名
`(Ljava/lang/String;Ljava/lang/String;IIJ)V` 与 `(Ljava/lang/String;Ljava/lang/String;II)V`
在全部 17 个 dex 中 0 命中。所以"纯改名"假设不成立，必须有行为验证。

## 2. 目标

1. **跟随新版**：微信升级后，尽量不改代码就能恢复收发；
2. **兼容老版**：已支持的版本行为完全不变；
3. **失败可见**：新版跑不起来时，日志/后台直接告诉你是哪个环节缺了。

## 3. 架构

```
HookEntry（与版本无关的稳定核心）
  · 观测 hook：WCDB insertWithOnConflict / sActiveDatabases      ← 非混淆名，天然稳定
  · 身份识别：userinfo id=2/42 + looksLikeAccountId              ← 非混淆名
  · outbox 轮询 / ack                                            ← HTTP，与微信无关
  · 发送：只调用 SendAdapter 接口，具体 binding 由解析层给出
            │
            ├─ L0 Profile 精确命中（versionCode / dexHash）        ← 老版本永远走这条
            ├─ L1 结构定位（dex 扫描：形状 + 类型引用 + 成员特征）
            └─ L2 Canary 验证（真发 filehelper + 查 message 表落库）← 决定性判据
            │
     BindingCache (versionCode + dexHash) ──► gateway 下发/共享
```

### 3.1 Profile（版本知识数据化）

一个微信版本 = 一段 JSON，新增版本只加数据、不改代码：

```json
{
  "wechat": { "versionName": "8.0.74", "versionCode": "3120" },
  "dexHash": "sha256:...",
  "identity": {
    "idQueries": [
      "SELECT value FROM userinfo WHERE id=2 LIMIT 1",
      "SELECT value FROM userinfo WHERE id=42 LIMIT 1"
    ],
    "nicknameQueries": [
      "SELECT value FROM userinfo WHERE id=4 LIMIT 1",
      "SELECT value FROM userinfo WHERE id=5 LIMIT 1"
    ]
  },
  "send": {
    "preferred": "builder",
    "paths": [
      { "id": "builder", "factory": "w11.s1", "builder": "w11.r1",
        "queue": "w11.n1", "localIdField": "f459357f" },
      { "id": "netscene", "class": "w11.r0",
        "ctor": "(Ljava/lang/String;Ljava/lang/String;IIJ)V",
        "enqueue": "com.tencent.mm.modelbase.z2#b",
        "localIdField": "f459357f" },
      { "id": "event", "class": "com.tencent.mm.autogen.events.SendMsgEvent",
        "payloadField": ["f71992g", "g"],
        "payload": { "wxid": "f7337a", "text": "f7338b", "type": "f7339c", "flag": "f7340d" },
        "dispatch": ["e"] },
      { "id": "sendmgr", "accessor": "tg3.t1#a", "impl": "dk5.s5",
        "method": "(Ljava/lang/String;Ljava/lang/String;II)V" }
    ],
    "bootstrap": {
      "needed": true,
      "registry": { "class": "fs.g", "slot": ["f283324a", "a"] },
      "kernel": { "class": "i95.n0", "flag": ["f307062f", "f"] }
    }
  }
}
```

约定：
- 所有名字字段支持**候选数组**（老版本兼容 + 新版顺延）；
- 签名类字段（`ctor` / `method`）写**形状**而不是名字，供 L1 使用；
- 新版本 profile 追加，**永不修改/删除已有版本**。

### 3.2 三级解析

| 级别 | 输入 | 输出 | 成本 |
|---|---|---|---|
| L0 | profile 命中 | 直接 binding | ~0 |
| L1 | 运行时 loader 的 dex | 候选集（带打分） | 实测 17 dex / 178MB ≈ 20s（Python），端上流式解析预计 3~10s，一次性并缓存 |
| L2 | 候选 + canary 发送 | 锁定的 binding | 秒级，每小时 ≤2 次，仅灰度机 |

L1 的候选特征（按可靠性排序）：
1. 引用 `com.tencent.mm.autogen.events.SendMsgEvent`（非混淆名）的类；
2. 被 `com.tencent.mm.modelbase.z2#b(m1)` 调用点引用的类（入队语义，非混淆名）；
3. 含 `(String,String,int…)` 形状方法、且方法返回 void 的类；
4. 持有 `long` 类型「本地消息 id」字段、且构造器含两个 String 的类。

L2 判据：向 `filehelper` 发一条探针文本 → 查本地 `message` 表是否新增 outgoing 行
（模块已有 `sendText confirmed by local message status` 的能力）→ 成功即锁定并缓存。

> 8.0.78 的实测说明 L1 单独不够（旧形状 0 命中），**L2 是必需环节**，不是可选优化。

### 3.3 服务端

| 接口 | 作用 |
|---|---|
| `POST /module/diagnostics` | 上报 `{versionName, versionCode, dexHash, 各能力探测结果}` |
| `GET /module/profiles?versionCode=3180` | 下发该版本 profile（含自动生成的候选） |
| `POST /module/profiles/verify` | 提交 canary 验证成功的 binding，后台一键转正 |

后台新增「微信版本矩阵」页：一行一个版本，列为 observation / identity / send / bootstrap，
显示 ✔️/❌、最近上报时间、失败原因；新版首次上报即标红，不再需要 adb。

### 3.4 发布闭环

```
微信发新版 → 灰度机升级 → L1 解析候选 → L2 canary 验证
        → 上报 gateway（矩阵显示"候选可用"）→ 管理员转正 → profile 下发
        → 其余设备升级即可用；期间老版本设备继续走 L0，不受影响
```

## 4. 兼容老版本的保证

1. 观测/身份/outbox 不依赖混淆名，保持单一实现、零分支；
2. 发送收敛为 `SendAdapter.send(wxid, text, type) -> msgId`，各路径只差 binding，老路径不删除；
3. profile 是数据，历史版本条目只追加；
4. CI 回归：`tools/wechat_hook_probe.py <apk>` 对每个受支持版本断言 hook 点齐全（实测 20s/包），退出码非 0 即失败。

## 5. 已知无法自动化的部分

`ensureWeChatRegistries`（唤醒微信内核单例：`fs.g#f283324a`、`i95.n0#f307062f`、
`com.tencent.mm.app.p0#f70808d`）没有可靠的形状特征，自动推断有把微信搞崩的风险。策略：

1. 优先选择**不需要内核引导**的路径（`SendMsgEvent` 走 autogen 事件总线，名字非混淆，优先押注）；
2. 需要时按"静态单例访问器 + canary 验证"生成候选；
3. 兜底：新版标记为"需人工适配"并在后台显式提示，而不是静默失效。

## 6. 已实现（P1）

### 6.1 `tools/wechat_hook_probe.py`

```bash
# 检查某个微信包是否还包含模块需要的全部 hook 点（退出码非 0 = 有缺失）
python tools/wechat_hook_probe.py weixin.apk

# 生成 profile 骨架 + 结构候选
python tools/wechat_hook_probe.py weixin.apk --json profile.json --shape-scan
```

- hook 点清单里 **stable 项**（WCDB、modelbase、SendMsgEvent 等非混淆名）必须全绿；
- **obfuscated 项**缺失即对应能力失效；
- 会顺便从 `HookEntry.java` 提取字面量交叉验证，避免清单与代码漂移；
- 依赖：仅标准库；有 `aapt2` 时读版本号，没有就从 manifest 的 UTF-16 字符串里取。

### 6.2 模块启动自检

`HookEntry.runWorker()` 启动时输出一行（每进程一次，行为不变）：

```
capability report: wechat=8.0.78/3180 observation=ok identity=ok
  send.builder=missing(ClassNotFoundException: w11.s1) send.netscene=missing(...)
  send.event=ok send.mgr=missing(...) bootstrap=missing(NoSuchFieldException: f283324a)
```

`send.*` 全 missing 就是"新版微信下只能收不能发"的指纹。

## 7. 后续（P2 / P3）

| 阶段 | 内容 |
|---|---|
| P2 | 抽出 profile 读取层；内置 `assets/profiles/`（8.0.74/75/76/78）；服务端下发 + 本地缓存。行为与现在完全一致，零回归 |
| P3 | L1 结构定位 + L2 canary 自验证 + 映射缓存上报 + 后台矩阵页与一键转正 |

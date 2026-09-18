# Android 模块的微信版本自适应方案

状态：**P1 已实现**（探针工具 + 模块启动自检），P2/P3 为设计。
关联代码：`android-module/`、`tools/wechat_hook_probe.py`

## 1. 问题

模块通过**反射调用微信内部类/字段**来收发消息。微信每个版本用新的随机种子重新混淆
（`-repackageclasses`），类名、字段名全部改变，因此：

- 写死名字 = 版本锁死；
- 版本一换，发送链路静默失效（观测还能用，回复发不出去）；
- 现有代码在 `findClass` 失败时静默 fallback，线上只能靠 adb 抓 logcat 排查。

实测（`tools/wechat_hook_probe.py`，8.0.74 用的是从手机导出的官方包）：

| 微信版本 | versionCode | observation | identity | 可用发送路径 | bootstrap | 结论 |
|---|---|---|---|---|---|---|
| 8.0.74 | 3120 | ok | ok | builder, netscene, event, sendmgr | **ok** | 可用（真机验证） |
| 8.0.76 | 3141 | ok | ok | event | **incomplete: i95.n0, i95.y** | 不可用 |
| 8.0.78 | 3180 | ok | ok | event | **incomplete: i95.n0, i95.y** | 不可用 |

判定规则（已与真机行为交叉验证）：

1. `observation` / `identity` 用非混淆名，缺失即致命；
2. **`bootstrap` 是真正的门控**：`HookEntry.isWeChatReadyForSend()` 必须先解析
   `fs.g` 注册表槽位与 `i95.n0` 内核标志，否则直接跳过 outbox 投递，表现就是
   "能收不能发"。8.0.76 / 8.0.78 正好缺 `i95.n0`、`i95.y`；
3. 混淆**字段名不能作为判据**：8.0.74（正常工作的版本）里这些字段名同样"缺失"，
   因为模块本身有短名回退（`findFieldAny(..., "f283324a", "a")`）。工具把它们
   列为 advisory，仅作提示。

设备侧交叉验证：8.0.74 真机 capability 报告为
`observation=ok send.builder=ok send.netscene=ok send.event=ok send.mgr=ok bootstrap=ok`，
与探针结论一致。

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

## 4. 实际采用的流程：插 USB 直接适配（不做自动分发/灰度）

结论：不做 gateway 下发 profile、不做 canary 灰度。微信更新时按下面四步人工适配，
工具负责**把"改哪里"直接指出来**：

```bash
# 1) 手机插上 USB，直接拉当前微信包并诊断（退出码非 0 = 需要适配）
python tools/wechat_hook_probe.py --from-device

# 2) 与上一个能用的版本对比，列出丢掉的类和改名的成员
python tools/wechat_hook_probe.py --from-device --compare wechat-8.0.74-base.apk

# 3) 按报告改 HookEntry.java 里的类名/字段名，必要时更新 assets/profiles/

# 4) 重新构建 + 安装，看 capability report 是否全 ok
python tools/wechat_hook_probe.py --from-device --profile \
       android-module/app/src/main/assets/profiles/wechat.json   # 需要时刷新 profile
```

`--compare` 会输出三类信息：

- **LOST**：上个版本有、新版本没有的类（需要重新定位）；
- **成员变化**：类还在但方法/字段被改名或改签名（例如
  `SendMsgEvent.g: Lam/mt; -> Lfm/xt;` 说明 payload 类被换包；
  `fs.g` 从"带静态字段的类"变成 **enum**，所以 `f283324a` 必然不存在）；
- **无差异**：这些 hook 点可以直接复用。

## 5. 兼容老版本的保证

1. 观测/身份/outbox 不依赖混淆名，保持单一实现、零分支；
2. 发送仍是"多路径依次尝试"，新版本加路径即可，老路径不删除；
3. CI 回归：`tools/wechat_hook_probe.py <apk>` 对每个受支持版本断言
   observation / identity / bootstrap 全绿（实测 20s/包），退出码非 0 即失败。

## 6. 已知无法自动化的部分

`ensureWeChatRegistries`（唤醒微信内核单例：`fs.g`、`i95.n0`、
`com.tencent.mm.app.p0`）没有可靠的形状特征，自动推断有把微信搞崩的风险。
`--compare` 只能告诉你它变了（8.0.78 里 `i95.n0`/`i95.y` 直接消失、`fs.g` 变成 enum），
具体怎么改仍需人工判断。

## 7. 已实现

### 7.1 `tools/wechat_hook_probe.py`

```bash
# 检查某个微信包是否还包含模块需要的全部 hook 点（退出码非 0 = 有缺失）
python tools/wechat_hook_probe.py weixin.apk

# 直接从 USB 设备拉当前微信包再检查
python tools/wechat_hook_probe.py --from-device

# 与上一个可用版本对比
python tools/wechat_hook_probe.py weixin.apk --compare wechat-8.0.74-base.apk

# 生成 profile + 结构候选
python tools/wechat_hook_probe.py weixin.apk --profile out.json --shape-scan
```

判定规则：**observation / identity / bootstrap 是硬门槛**，任一不过退出码非 0；
`send` 只作信息展示（列出还完整的路径）；混淆字段名一律按 advisory 处理，
因为能正常工作的 8.0.74 里它们同样"缺失"（模块有短名回退）。

### 7.2 模块启动自检

`HookEntry.runWorker()` 启动时输出一行（每进程一次，行为不变）：

```
capability report: wechat=8.0.78/3180 observation=ok identity=ok
  send.builder=missing(ClassNotFoundException: w11.s1) send.netscene=missing(...)
  send.event=ok send.mgr=missing(...) bootstrap=missing(NoSuchFieldException: f283324a)
```

`send.*` 全 missing，或 `bootstrap=missing(...)`，就是"新版微信下只能收不能发"的指纹。

### 7.3 版本档案（assets/profiles/）

`wechat.8.0.74.json`（手机导出包生成，可用）、`wechat.8.0.76.json`、
`wechat.8.0.78.json`（后两者 bootstrap 不完整、不可用）。这些文件当前只是**记录**，
模块仍在代码里解析 hook 点；后续如果要让模块直接读它们，再单独做读取层。

## 8. 明确不做的部分

- 不做 gateway 下发 profile、不做 canary 灰度、不做映射缓存共享；
- 不做无障碍/企业微信等替代通道。

版本适配按第 4 节的"插 USB 四步走"人工完成，工具负责定位差异。

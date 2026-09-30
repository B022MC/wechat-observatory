# Android 模块的微信版本自适应方案

状态：**P1 已实现**（探针工具 + 模块启动自检），P2/P3 为设计。
关联代码：`android-module/`、`tools/wechat_hook_probe.py`

## 1. 问题

模块通过**反射调用微信内部类/字段**来收发消息。微信每个版本用新的随机种子重新混淆
（`-repackageclasses`），类名、字段名全部改变，因此：

- 写死名字 = 版本锁死；
- 版本一换，发送链路静默失效（观测还能用，回复发不出去）；
- 现有代码在 `findClass` 失败时静默 fallback，线上只能靠 adb 抓 logcat 排查。

历史探针结果（0.1.6；其中 identity 只检索 SQL 字符串，未验证当前登录归属；
0.1.7 身份校验以第 9 节为准）：

| 微信版本 | versionCode | observation | identity | 可用发送路径 | bootstrap | 结论 |
|---|---|---|---|---|---|---|
| 8.0.74 | 3120 | ok | ok | builder, netscene, event, sendmgr | **ok** | 可用（真机验证） |
| 8.0.76 | 3141 | ok | ok | event | **incomplete: i95.n0, i95.y** | 不可用 |
| 8.0.78 | 3180 | ok | ok | event | **incomplete: i95.n0, i95.y** | 不可用 |

判定规则（已与真机行为交叉验证）：

1. `observation` 检查 WCDB；0.1.7 的 `identity` 校验当前内核与 ConfigStorage 方法；
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
  · 身份识别：当前内核的 ConfigStorage key=2 / nickname key=4    ← 需要版本绑定
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
    "source": "current_kernel_config",
    "usernameKey": 2,
    "nicknameKey": 4
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

1. 观测/outbox 使用公共接口；身份采用已核验的当前内核绑定，未知版本等待适配；
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

版本适配按第 4 节的"插 USB 四步走"人工完成，工具负责定位差别。

## 9. 当前账号隔离与“未收录”修复（0.1.7）

“未收录”表示按 `device + owner_wxid` 查询的有效联系人中找不到会话对端。
原因可能是 owner 错配、快照停更、联系人已删除或确实未收录；仅看在线 `ready`
状态或按设备联表，不足以判断联系人同步成功。

### 61538E 的核实结论

0.1.6 以联系人数量和 API Key 保存的旧身份挑库。旧登录数据库仍打开时，
它可能胜出；跨库补读群聊还会让空库看起来有联系人。目录哈希、消息编号连续、
通讯录重合均不足以证明当前登录账号。用户报告当前账号可能为“雨雨”，
服务器有两个不同 wxid 同名，现有旧通讯录则指向“兰兰”。

此前合并将 180 行联系人去重为 91 行，并重写了 12 条旧消息 owner。
35 条备份消息的其他字段保持原样。实际有效联系人接口仅匹配 4 个会话中的 2 个；
已删除联系人被排除，所以此前“3/4 已恢复”的结论不成立。

### 当前内核读取

| 微信版本 | 当前 core storage | 主库路径 | ConfigStorage | getter |
| --- | --- | --- | --- | --- |
| 8.0.74 | `gm0.j1.u() -> gm0.b0` | `g()` | `c() -> storage.n3` | `l(int,Object)` |
| 8.0.78 | `gp0.j1.x() -> gp0.b0` | `g()` | `c() -> storage.q3` | `m(int,Object)` |

主线程读取 key 2 的正式账号 ID、key 4 的昵称和 `g()` 返回的主库路径。
`g()` 经 `h()` 调用内核账号初始化检查；8.0.78 配置可由 MMKV 承载，
直接查 SQL `userinfo` 不是可靠身份来源。key 42 是可变别名，不作为 owner 回退。
ID 缺失、读取中账号改变或版本绑定缺失时，等待重试。

`RuntimeAccount` 持有当前账号目录、库句柄、消息游标、重放额度和同步时间。
只探测同一规范化目录内的 `rcontact` / `message`；联系人及群聊可补读同目录分库。
媒体读取也限定在该目录。旧目录数据量更大不会改变选择。
切号后新建上下文；上报、注册和主线程发送前重新核验账号，旧 outbox 任务失败退出。
注册与上报串行，避免并发注册改变服务端推断的 owner。
仅主进程运行采集和注册；插入钩子复制数据后异步处理，避免占着数据库锁等待主线程。

零联系人或查询失败时保留服务端旧快照并重试。观察链路独立于发送适配 readiness。
新账号轮询游标初始化到其当前最大消息 ID；插入钩子上传不推进有序轮询游标，
以免跳过较早失败的上报。首轮游标初始化前的窗口仍需手机联调验证。

### 校验与上线顺序

```powershell
# 校验当前账号绑定的方法定义、返回类型和账号目录检查
python tools/wechat_account_probe.py path/to/weixin.apk
# 综合探针也调用当前账号校验（需要 androguard）
python tools/wechat_hook_probe.py path/to/weixin.apk
```

1. 停用 61538E 旧模块，在批准执行的恢复窗口按下面的工具预检并还原备份范围内数据。
2. 先部署支持持久化账号会话的服务端，再安装 `0.1.8-account-sessions`（versionCode 9）调试包，按现有 Vector/LSPosed 流程确认模块加载。详见 [账号会话隔离与升级顺序](account-session-isolation.md)。
3. 登录目标账号，核对日志 `current WeChat account wxid=... nickname=... path=...`，
   并确认对应 owner 出现新的 `contact sync uploaded`。这一步自动识别 wxid，避免凭昵称猜。
4. 切换两个账号验证联系人、消息与发送隔离；发送测试需用户授权。
5. 新的账号映射另行依据手机证据处理。

8.0.74 / 8.0.78 的方法形状已校验；8.0.76 尚未添加当前账号绑定。
APK 形状校验和 JVM 测试不代替手机运行验证，也不证明全部发送路径可用。

### 备份范围内的历史恢复

`tools/restore_61538e_owner_merge.py` 默认只读，固定处理此次 61538E 事故。
连接配置来自 `OBS_DB_HOST/USER/PASSWORD/NAME`（可选 `OBS_DB_PORT`）。
会话时区设为 `+08:00`，与事故记录一致。

预检必须逐列匹配 03:06:02 合并结果：91 行保留联系人、35 条备份消息。
任何新增联系人快照、内容变动、缺失或 ID 冲突都会使执行停止。
恢复后保留原始两个 owner 的 180 行联系人及删除标志；12 条旧消息还原 owner；
备份之后的新消息保持原样。恢复原始 hash 分区不等于确认 hash 对应哪个真实账号。

```bash
# 先停用旧模块，避免恢复后再次写入错误快照；默认 dry-run
python tools/restore_61538e_owner_merge.py
# 经审阅后，在同一连接配置下执行；指纹来自本次预览，备份文件须为新路径
python tools/restore_61538e_owner_merge.py --apply \
  --expected-plan REVIEWED_SHA256 --recovery-backup NEW_RECOVERY_BACKUP.json
```

Apply 使用 InnoDB 事务和行锁，在第一次写入前落盘并 fsync 新备份；失败整体回滚。
新消息增加不会改变预览指纹；目标旧记录变化会使指纹或前置条件失败。
已完整恢复的状态会返回 no-op。工具没有按设备全量统一 owner 的操作。
若新模块已刷新联系人导致预检失败，应重新审阅快照，不应跳过校验强行恢复。

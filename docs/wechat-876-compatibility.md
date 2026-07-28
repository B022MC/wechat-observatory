# WeChat 8.0.76 兼容性分析

## 问题现象

Android 模块在微信 8.0.76 上持续输出：

```
WeChat readiness: readiness check failed: fs.g
WeChat send stack not ready; skip outbox websocket
WeChat send stack not ready; skip outbox poll
```

结果：

- 发消息（outbox）完全阻塞
- 消息轮询（`pollMessagesIfDue`）被同一检查门控阻塞
- 收消息实时 Hook（`handleInsert`）不受影响，但因无法轮询历史，首次启动后的消息补漏也失效

---

## 根本原因

`HookEntry.isWeChatReadyForSend()` 的就绪检查（`HookEntry.java:441`）尝试加载：

```java
findClass(classLoader, "fs.g")      // extension registry
findClass(classLoader, "i95.n0")    // service manager
```

这两个混淆类名在 8.0.76 里已被重命名，`ClassNotFoundException` 导致整个就绪检查永远返回 `false`。

---

## 8.0.75 → 8.0.76 类名变化详情

以下数据来自对 8.0.76 base APK（17 个 dex，共约 170MB）的分析。

### 1. 稳定类（未变）

| 类名 | 作用 | 所在 dex |
|------|------|----------|
| `com.tencent.mm.sdk.platformtools.x2` | MMApplicationContext | 多个 dex |
| `com.tencent.mm.app.p0` | Provider 类，持有 Application 入口 | classes4.dex |
| `com.tencent.mm.app.q0` | 枚举 `INSTANCE` | classes4.dex |
| `k95.a` | 服务管理器初始化第三参数类型 | classes11.dex |

字段名 `f210311a`（`x2` 上的静态 Application 字段）需要在8.0.76运行时验证是否变化。

### 2. 完全消失的类

| 8.0.75 类名 | 作用 |
|------------|------|
| `fs.g` | Extension registry（静态数组字段 `f283324a`/`a`，初始化判断依据）|
| `fs.k2` | 枚举，`INSTANCE` 常量 |
| `i95.n0` | Service manager（静态布尔标志 `f307062f`/`f`，初始化静态方法 `d(Application, i95.y, k95.a)`）|
| `i95.y` | Service manager 初始化第二参数类型 |
| `w11.r0` | Classloader 探测 |
| `w11.s1` | 发消息入口（方法 `a(String)` → `w11.r1`）|
| `w11.r1` | 发消息构建器（方法 `g/e/h`，链接到 `w11.n1`）|
| `w11.n1` | NetScene 队列准入 |
| `tg3.t1` | 发消息回退入口 |
| `dk5.s5` | 发消息回退方法 `fj(...)` |

### 3. 包名存活但类名变化

#### `fs` 包

8.0.75 有 `fs.g`、`fs.k2`。8.0.76 有：

| 类 | 超类 | 说明 |
|----|------|------|
| `fs.a` | `La44/a` | Kotlin 协程包装，**不是 extension registry** |
| `fs.b` | `La44/a` | Kotlin 协程包装 |
| `fs.c` | `La44/a` | Kotlin 协程包装 |

**结论**：`fs` 包在 8.0.76 里用途已变，extension registry 已移至其他包。

#### `i95` 包

8.0.75 有 `i95.n0`、`i95.y`。8.0.76 有：

| 类 | 所在 dex |
|----|----------|
| `i95.a` – `i95.d` | classes6.dex |
| `i95.e` | classes8.dex |
| `i95.f` – `i95.i` | classes14.dex |

哪个是 service manager（需要静态方法 `?(Application, ?, k95.a)`）尚未确认。

#### `k95` 包

8.0.75 只用 `k95.a`。8.0.76 的 `k95` 包规模大幅扩展（`a`–`z`、`a0`–`z0`、`j1`/`k1`/`l1`/`n1`/`o1`/`r0`/`y0` 等），分布在 classes5、classes6、classes11、classes15、classes16 中。

---

## 未确认的映射

以下映射需要进一步分析（运行时调试或完整 dex 反编译工具）：

| 8.0.75 | 作用 | 8.0.76 候选 | 确认方法 |
|--------|------|------------|---------|
| `fs.g` | Extension registry | 未知，需搜索有静态 Object[] 字段的类 | 运行时反射枚举 + jadx |
| `i95.n0` | Service manager | `i95.a`–`i95.i` 中某一个 | 搜索含 `k95.a` 参数类型的静态方法 |
| `i95.y` | SM 初始化参数 | 未知 | 从 `p0` 提供者方法返回类型追踪 |
| `w11.s1` | 发消息入口 | 完全未知，需重新搜索 | jadx 反编译 + Tinker patch 分析 |
| `w11.r1` | 发消息构建器 | 完全未知 | 同上 |

---

## 建议的修复方案

### 方案 A：找到新类名并更新硬编码（彻底修复）

1. 拉取 Tinker patch 做差量分析：
   ```
   adb pull /data/user/0/com.tencent.mm/tinker/patch-*/dex/tinker_classN.apk
   ```
   Tinker patch 只包含变更类，比完整 APK 小得多，是找 send builder 的优先目标。

2. 用 jadx 反编译（在有网络或代理的环境）：
   ```
   jadx -d out/ tinker_classN.apk
   jadx -d out-base/ wechat-8.0.76-base.apk
   ```

3. 定位 extension registry：搜索 `com.tencent.mm.app.p0` 的反编译代码，找它初始化时引用的外部短类名。

4. 定位 service manager：搜索接受 `k95.a` 为参数类型的静态方法。

5. 定位 send builder：搜索包含 `Application.getClassLoader()` + 构建消息逻辑的新类（因为8.0.76 Tinker 路径仍存在）。

6. 更新 `HookEntry.java` 中的所有硬编码类名和字段名，重新编译安装。

### 方案 B：运行时自动探测（中期缓解）

在 `isWeChatReadyForSend()` 里改为扫描已加载类，而不是硬编码类名：

```java
// 遍历 classloader 里已加载的类，找静态 Object[] 字段且数组第0槽非空的类
// 替代 findClass(classLoader, "fs.g")
```

这样模块能在新版本自动适配，但实现复杂度较高。

### 方案 C：降级就绪检查（快速临时解决，只恢复 outbox）

把 `isWeChatReadyForSend` 的两个硬编码检查改成 try-catch fail-open：若找不到类则直接返回 `true`，让 outbox 尝试发送，通过异常结果判断是否成功。此方案可能导致发送失败率上升，但不会崩溃，且恢复轮询。

---

## 当前测试状态

- **实时消息 Hook**（`insertWithOnConflict`）：已挂钩，预计可以捕获新到消息，但 `bridge_message_events` 当前为 0——需要主动在微信收一条消息后确认。
- **outbox 发送**：完全阻塞，直到类名更新。
- **消息轮询（`pollMessagesIfDue`）**：被同一检查阻塞。
- **联系人同步**：同样被阻塞。
- **设备注册**：正常（`last_register_at` 有更新）。

---

## 分析环境

- 设备：小米 Xiaomi 14（device=`prod-bootstrap`，wxid=`wxid_m429rol07fbc22`）
- 微信版本：8.0.76（含 Tinker patch `patch-ebb0a306`）
- APK 大小：266MB，17 个 dex
- 分析日期：2026-07-24

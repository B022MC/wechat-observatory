package cc.wechat.observatory;

import android.content.ContentValues;
import android.content.Context;
import android.app.Application;
import android.os.Handler;
import android.os.Looper;
import android.util.Base64;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.lang.reflect.Field;
import java.lang.reflect.Constructor;
import java.lang.reflect.Method;
import java.lang.reflect.Modifier;
import java.net.HttpURLConnection;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.Callable;
import java.util.concurrent.FutureTask;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLHandshakeException;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

import cc.wechat.observatory.config.BridgeConfig;
import cc.wechat.observatory.gateway.GatewayEndpoint;
import cc.wechat.observatory.gateway.WebSocketFrame;
import cc.wechat.observatory.model.MessagePayload;
import cc.wechat.observatory.util.BridgeLogger;
import cc.wechat.observatory.wechat.LocalMessageConfirmation;
import cc.wechat.observatory.wechat.QueueSubmissionRetrier;
import cc.wechat.observatory.wechat.SendResult;
import de.robv.android.xposed.IXposedHookLoadPackage;
import de.robv.android.xposed.XC_MethodHook;
import de.robv.android.xposed.XposedBridge;
import de.robv.android.xposed.XposedHelpers;
import de.robv.android.xposed.callbacks.XC_LoadPackage;

import static cc.wechat.observatory.util.Strings.isBlank;
import static cc.wechat.observatory.util.Strings.json;
import static cc.wechat.observatory.util.Strings.shortError;
import static cc.wechat.observatory.util.Strings.trimRight;

public final class HookEntry implements IXposedHookLoadPackage {
    private static final String WECHAT_PACKAGE = "com.tencent.mm";
    private static final AtomicBoolean WORKER_STARTED = new AtomicBoolean(false);
    private static final AtomicBoolean OUTBOX_WORKER_STARTED = new AtomicBoolean(false);
    private static volatile String LAST_READY_STATE = "";
    private static volatile String LAST_CLASSLOADER_STATE = "";
    private static volatile Object LAST_DATABASE;
    private static volatile long LAST_CONTACT_SYNC_AT = 0L;
    private static volatile long LAST_CONTACT_SYNC_SKIP_LOG_AT = 0L;
    private static volatile long LAST_CONTACT_SCAN_LOG_AT = 0L;
    private static volatile long LAST_MESSAGE_POLL_AT = 0L;
    private static volatile long LAST_MESSAGE_ID = 0L;
    private static volatile boolean MESSAGE_WATERMARK_READY = false;
    private static volatile long LAST_WEBSOCKET_FAIL_LOG_AT = 0L;
    private static volatile String CURRENT_WXID = "";
    private static volatile String CURRENT_NICKNAME = "";
    private static volatile String REGISTERED_KEY = "";
    private static volatile String REGISTERED_DEVICE = "";
    private static volatile long LAST_REGISTER_ATTEMPT_AT = 0L;
    private static volatile long LAST_REGISTER_SUCCESS_AT = 0L;
    private static volatile Context APP_CONTEXT;
    private static volatile ClassLoader WECHAT_CLASS_LOADER;
    private static final int SEND_QUEUE_MAX_ATTEMPTS = 5;
    private static final long SEND_QUEUE_RETRY_DELAY_MS = 500L;
    private static final int SEND_STATUS_MAX_CHECKS = 10;

    @Override
    public void handleLoadPackage(XC_LoadPackage.LoadPackageParam lpparam) {
        if (!WECHAT_PACKAGE.equals(lpparam.packageName)) {
            return;
        }
        boolean mainProcess = WECHAT_PACKAGE.equals(lpparam.processName);

        try {
            Class<?> sqliteDatabase = XposedHelpers.findClass("com.tencent.wcdb.database.SQLiteDatabase", lpparam.classLoader);
            XposedHelpers.findAndHookMethod(
                    sqliteDatabase,
                    "insertWithOnConflict",
                    String.class,
                    String.class,
                    ContentValues.class,
                    int.class,
                    new XC_MethodHook() {
                        @Override
                        protected void afterHookedMethod(MethodHookParam param) {
                            handleInsert(param);
                        }
                    });
            hookDatabaseCaptureMethods(sqliteDatabase);
            log("hooked WeChat WCDB access methods");
            if (mainProcess) {
                hookApplicationAttach(lpparam.classLoader);
                hookWeChatApplication(lpparam.classLoader);
            } else {
                log("skip outbox worker in process " + lpparam.processName);
            }
        } catch (Throwable t) {
            log("hook failed: " + t);
        }
    }

    private static void hookDatabaseCaptureMethods(Class<?> sqliteDatabase) {
        for (Method method : sqliteDatabase.getDeclaredMethods()) {
            String name = method.getName();
            if (!shouldHookDatabaseMethod(name) || Modifier.isStatic(method.getModifiers())) {
                continue;
            }
            try {
                method.setAccessible(true);
                XposedBridge.hookMethod(method, new XC_MethodHook() {
                    @Override
                    protected void beforeHookedMethod(MethodHookParam param) {
                        captureDatabaseFromArgs(param.thisObject, param.args);
                    }

                    @Override
                    protected void afterHookedMethod(MethodHookParam param) {
                        captureDatabaseFromArgs(param.thisObject, param.args);
                    }
                });
            } catch (Throwable t) {
                log("hook db method " + name + " failed: " + shortError(t));
            }
        }
    }

    private static boolean shouldHookDatabaseMethod(String name) {
        return "rawQuery".equals(name)
                || "rawQueryWithFactory".equals(name)
                || "query".equals(name)
                || "queryWithFactory".equals(name)
                || "insert".equals(name)
                || "insertOrThrow".equals(name)
                || "replace".equals(name)
                || "replaceOrThrow".equals(name)
                || "update".equals(name)
                || "updateWithOnConflict".equals(name)
                || "delete".equals(name);
    }

    private static void captureDatabaseFromArgs(Object db, Object[] args) {
        if (db == null || args == null) {
            return;
        }
        for (Object arg : args) {
            if (arg instanceof String && looksLikeContactOrMessageAccess((String) arg)) {
                if (LAST_DATABASE != db) {
                    LAST_DATABASE = db;
                    log("captured WeChat database from " + shorten((String) arg, 80));
                }
                return;
            }
        }
    }

    private static boolean looksLikeContactOrMessageAccess(String value) {
        if (isBlank(value)) {
            return false;
        }
        String normalized = value.trim().toLowerCase(Locale.US);
        return "message".equals(normalized)
                || "rcontact".equals(normalized)
                || normalized.contains(" from message")
                || normalized.contains(" from rcontact")
                || normalized.startsWith("select ") && normalized.contains("rcontact")
                || normalized.startsWith("select ") && normalized.contains("message");
    }

    private static String shorten(String value, int maxLength) {
        if (value == null || value.length() <= maxLength) {
            return value;
        }
        return value.substring(0, maxLength) + "...";
    }

    private static void hookApplicationAttach(final ClassLoader classLoader) {
        try {
            XposedHelpers.findAndHookMethod(
                    Application.class,
                    "attach",
                    Context.class,
                    new XC_MethodHook() {
                        @Override
                        protected void afterHookedMethod(MethodHookParam param) {
                            if (param.args != null && param.args.length > 0 && param.args[0] instanceof Context) {
                                APP_CONTEXT = (Context) param.args[0];
                                ClassLoader runtimeLoader = param.thisObject == null ? null : param.thisObject.getClass().getClassLoader();
                                log("Application.attach captured context; start outbox worker");
                                startWorker(runtimeLoader == null ? classLoader : runtimeLoader);
                            }
                        }
                    });
            log("hooked Application.attach");
        } catch (Throwable t) {
            log("hook Application.attach failed: " + t);
        }
    }

    private static void hookWeChatApplication(final ClassLoader classLoader) {
        try {
            XposedHelpers.findAndHookMethod(
                    "com.tencent.mm.app.MMApplicationLike",
                    classLoader,
                    "onBaseContextAttached",
                    Context.class,
                    new XC_MethodHook() {
                        @Override
                        protected void afterHookedMethod(MethodHookParam param) {
                            if (param.args != null && param.args.length > 0 && param.args[0] instanceof Context) {
                                APP_CONTEXT = (Context) param.args[0];
                            }
                            ClassLoader runtimeLoader = param.thisObject == null ? null : param.thisObject.getClass().getClassLoader();
                            log("WeChat application init completed; start outbox worker");
                            startWorker(runtimeLoader == null ? classLoader : runtimeLoader);
                        }
                    });
            XposedHelpers.findAndHookMethod(
                    "com.tencent.mm.app.MMApplicationLike",
                    classLoader,
                    "onCreate",
                    new XC_MethodHook() {
                        @Override
                        protected void afterHookedMethod(MethodHookParam param) {
                            Object app = currentApplication();
                            if (app instanceof Context) {
                                APP_CONTEXT = (Context) app;
                            }
                            ClassLoader runtimeLoader = param.thisObject == null ? null : param.thisObject.getClass().getClassLoader();
                            log("WeChat application onCreate completed; start outbox worker");
                            startWorker(runtimeLoader == null ? classLoader : runtimeLoader);
                        }
                    });
            log("hooked WeChat application init");
            startDelayedWorker(classLoader, 20000L);
        } catch (Throwable t) {
            log("hook WeChat application init failed, start worker with readiness gate: " + t);
            startWorker(classLoader);
        }
    }

    private static void startDelayedWorker(final ClassLoader classLoader, final long delayMs) {
        Thread thread = new Thread(new Runnable() {
            @Override
            public void run() {
                if (!sleepOnce(delayMs)) {
                    return;
                }
                log("delayed worker fallback fired");
                startWorker(classLoader);
            }
        }, "wechat-observatory-delayed-start");
        thread.setDaemon(true);
        thread.start();
    }

    private static void handleInsert(XC_MethodHook.MethodHookParam param) {
        try {
            String table = stringArg(param.args, 0);
            if (param.thisObject != null && ("message".equals(table) || "rcontact".equals(table))) {
                LAST_DATABASE = param.thisObject;
            }
            if (!"message".equals(table)) {
                return;
            }
            if (!(param.args[2] instanceof ContentValues)) {
                return;
            }

            ContentValues values = (ContentValues) param.args[2];
            String talker = values.getAsString("talker");
            String content = values.getAsString("content");
            Integer isSend = values.getAsInteger("isSend");
            Long msgId = values.getAsLong("msgId");
            Long createTime = values.getAsLong("createTime");
            Integer type = values.getAsInteger("type");
            String imgPath = values.getAsString("imgPath");

            int messageType = type == null ? 1 : type;
            if (!shouldReportMessage(talker, content, messageType)) {
                return;
            }

            BridgeConfig config = BridgeConfig.load(bridgeContext());
            if (!config.enabled || isBlank(config.baseUrl) || isBlank(config.apiKey)) {
                return;
            }
            if (!bindRuntimeIdentity(config)) {
                return;
            }
            if (!ensureRegistered(config)) {
                return;
            }

            MessagePayload payload = buildMessagePayload(config, talker, content, isSend, msgId, createTime, messageType, imgPath);
            post(config, payload);
            if (msgId != null && msgId > LAST_MESSAGE_ID) {
                LAST_MESSAGE_ID = msgId;
                MESSAGE_WATERMARK_READY = true;
            }
        } catch (Throwable t) {
            log("handle insert failed: " + t);
        }
    }

    private static void post(BridgeConfig config, MessagePayload payload) throws Exception {
        postJson(config, "/webhook/lsposed/message", payload.toJson());
    }

    private static void startWorker(final ClassLoader classLoader) {
        if (!WORKER_STARTED.compareAndSet(false, true)) {
            return;
        }
        startOutboxWorker(classLoader);
        Thread thread = new Thread(new Runnable() {
            @Override
            public void run() {
                runWorker(classLoader);
            }
        }, "wechat-observatory-worker");
        thread.setDaemon(true);
        thread.start();
    }

    private static void runWorker(ClassLoader classLoader) {
        WECHAT_CLASS_LOADER = classLoader;
        while (true) {
            BridgeConfig config = BridgeConfig.load(bridgeContext());
            try {
                if (config.enabled && !isBlank(config.baseUrl) && !isBlank(config.apiKey)) {
                    if (!bindRuntimeIdentity(config)) {
                        if (!sleepOnce(Math.max(3000L, config.pollIntervalMs))) {
                            return;
                        }
                        continue;
                    }
                    if (!ensureRegistered(config)) {
                        if (!sleepOnce(Math.max(3000L, config.pollIntervalMs))) {
                            return;
                        }
                        continue;
                    }
                    if (!isWeChatReadyForSend(classLoader)) {
                        log("WeChat send stack not ready; skip outbox poll");
                        if (!sleepOnce(Math.max(5000L, config.pollIntervalMs))) {
                            return;
                        }
                        continue;
                    }
                    syncContactsIfDue(config);
                    pollMessagesIfDue(config);
                }
            } catch (Throwable t) {
                log("worker failed: " + t);
            }
            if (!sleepOnce(Math.max(1000L, config.pollIntervalMs))) {
                return;
            }
        }
    }

    private static void startOutboxWorker(final ClassLoader classLoader) {
        if (!OUTBOX_WORKER_STARTED.compareAndSet(false, true)) {
            return;
        }
        Thread thread = new Thread(new Runnable() {
            @Override
            public void run() {
                runOutboxWorker(classLoader);
            }
        }, "wechat-observatory-outbox");
        thread.setDaemon(true);
        thread.start();
    }

    private static void runOutboxWorker(ClassLoader classLoader) {
        while (true) {
            BridgeConfig config = BridgeConfig.load(bridgeContext());
            try {
                if (config.enabled && !isBlank(config.baseUrl) && !isBlank(config.apiKey)) {
                    if (!bindRuntimeIdentity(config)) {
                        if (!sleepOnce(Math.max(3000L, config.pollIntervalMs))) {
                            return;
                        }
                        continue;
                    }
                    if (!ensureRegistered(config)) {
                        if (!sleepOnce(Math.max(3000L, config.pollIntervalMs))) {
                            return;
                        }
                        continue;
                    }
                    if (!isWeChatReadyForSend(classLoader)) {
                        log("WeChat send stack not ready; skip outbox websocket");
                    } else if (!runOutboxWebSocket(config, classLoader)) {
                        pollOutbox(config, classLoader);
                    }
                }
            } catch (Throwable t) {
                log("outbox worker failed: " + t);
            }
            if (!sleepOnce(Math.max(1000L, config.pollIntervalMs))) {
                return;
            }
        }
    }

    private static boolean sleepOnce(long millis) {
        try {
            Thread.sleep(millis);
            return true;
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
            return false;
        }
    }

    private static boolean isWeChatReadyForSend(ClassLoader classLoader) {
        try {
            classLoader = runtimeClassLoader(classLoader);
            WECHAT_CLASS_LOADER = classLoader;
            ensureWeChatRegistries(classLoader);
            if (!isStaticArraySlotReady(classLoader, "fs.g", "f283324a", "a")) {
                return readyState(false, "fs.g extension registry not initialized");
            }
            if (!isStaticBooleanFlag(classLoader, "i95.n0", "f307062f", "f")) {
                return readyState(false, "i95.n0 service manager not initialized");
            }
            return readyState(true, "ready");
        } catch (Throwable t) {
            return readyState(false, "readiness check failed: " + shortError(t));
        }
    }

    private static void ensureWeChatRegistries(ClassLoader classLoader) throws Exception {
        classLoader = runtimeClassLoader(classLoader);
        Class<?> appContext = findClass(classLoader, "com.tencent.mm.sdk.platformtools.x2");
        Object application = findFieldAny(appContext, "f210311a", "a").get(null);
        if (application == null) {
            application = currentApplication();
        }
        if (application == null) {
            log("WeChat application context is null during registry init");
            return;
        }
        if (findFieldAny(appContext, "f210311a", "a").get(null) == null) {
            findMethod(appContext, "u", Context.class).invoke(null, application);
            log("initialized WeChat MMApplicationContext from module");
        }

        Class<?> extensionRegistry = findClass(classLoader, "fs.g");
        Field registryField = findFieldAny(extensionRegistry, "f283324a", "a");
        Object registryArray = registryField.get(null);
        if (registryArray != null
                && registryArray.getClass().isArray()
                && java.lang.reflect.Array.getLength(registryArray) > 0
                && java.lang.reflect.Array.get(registryArray, 0) == null) {
            java.lang.reflect.Array.set(registryArray, 0, enumConstant(classLoader, "fs.k2", "INSTANCE"));
            findFieldAny(extensionRegistry, "f283326c", "c").set(null, application);
            findFieldAny(extensionRegistry, "f283325b", "b").set(null, enumConstant(classLoader, "com.tencent.mm.app.q0", "INSTANCE"));
            log("initialized fs.g extension registry from module");
        }

        if (!isStaticBooleanFlag(classLoader, "i95.n0", "f307062f", "f")) {
            Class<?> providerClass = findClass(classLoader, "com.tencent.mm.app.p0");
            Object provider = findFieldAny(providerClass, "f70808d", "d").get(null);
            Object y = findMethod(provider.getClass(), "b").invoke(provider);
            Method initialize = findMethod(
                    findClass(classLoader, "i95.n0"),
                    "d",
                    Application.class,
                    findClass(classLoader, "i95.y"),
                    findClass(classLoader, "k95.a"));
            initialize.invoke(null, application, y, enumConstant(classLoader, "com.tencent.mm.app.q0", "INSTANCE"));
            log("initialized i95.n0 service manager from module");
        }
    }

    private static Object enumConstant(ClassLoader classLoader, String className, String name) throws Exception {
        Class<?> enumClass = findClass(classLoader, className);
        Object[] constants = enumClass.getEnumConstants();
        if (constants != null && constants.length > 0) {
            for (Object constant : constants) {
                if (name.equals(String.valueOf(constant))) {
                    return constant;
                }
            }
            return constants[0];
        }
        Field field = findFieldAny(enumClass, name);
        return field.get(null);
    }

    private static Object currentApplication() {
        try {
            Class<?> activityThread = Class.forName("android.app.ActivityThread");
            Object app = findNoArgMethod(activityThread, "currentApplication").invoke(null);
            if (app != null) {
                return app;
            }
            Object thread = findNoArgMethod(activityThread, "currentActivityThread").invoke(null);
            return thread == null ? null : findNoArgMethod(thread.getClass(), "getApplication").invoke(thread);
        } catch (Throwable t) {
            log("read current application failed: " + shortError(t));
            return null;
        }
    }

    private static Context bridgeContext() {
        Context context = APP_CONTEXT;
        if (context != null) {
            return context;
        }
        Object app = currentApplication();
        return app instanceof Context ? (Context) app : null;
    }

    private static ClassLoader runtimeClassLoader(ClassLoader fallback) {
        List<ClassLoader> candidates = new ArrayList<>();
        addClassLoader(candidates, Thread.currentThread().getContextClassLoader());
        Object app = currentApplication();
        addClassLoader(candidates, app instanceof Application ? ((Application) app).getClassLoader() : null);
        addClassLoader(candidates, app == null ? null : app.getClass().getClassLoader());
        addClassLoader(candidates, fallback);

        ClassLoader loader = null;
        for (ClassLoader candidate : candidates) {
            if (candidate == null) {
                continue;
            }
            if (isPatchedWeChatLoader(candidate)) {
                loader = candidate;
                break;
            }
        }
        if (loader == null) {
            loader = firstLoadable(candidates);
        }
        if (loader == null) {
            return fallback;
        }
        String state = loader.toString();
        if (!state.equals(LAST_CLASSLOADER_STATE)) {
            LAST_CLASSLOADER_STATE = state;
            log("using WeChat runtime classLoader: " + state);
        }
        return loader;
    }

    private static void addClassLoader(List<ClassLoader> loaders, ClassLoader loader) {
        if (loader == null) {
            return;
        }
        for (ClassLoader existing : loaders) {
            if (existing == loader) {
                return;
            }
        }
        loaders.add(loader);
    }

    private static boolean isPatchedWeChatLoader(ClassLoader loader) {
        String description = String.valueOf(loader);
        return description.contains("DelegateLastClassLoader")
                || description.contains("tinker_classN")
                || description.contains("/tinker/");
    }

    private static ClassLoader firstLoadable(List<ClassLoader> candidates) {
        for (ClassLoader candidate : candidates) {
            try {
                Class.forName("w11.r0", false, candidate);
                Class.forName("i95.n0", false, candidate);
                return candidate;
            } catch (Throwable ignored) {
                // Try the next candidate.
            }
        }
        return null;
    }

    private static boolean readyState(boolean ready, String reason) {
        String state = ready ? "ready" : reason;
        if (!state.equals(LAST_READY_STATE)) {
            LAST_READY_STATE = state;
            log("WeChat readiness: " + state);
        }
        return ready;
    }

    private static boolean isStaticArraySlotReady(ClassLoader classLoader, String className, String... fieldNames) throws Exception {
        Field field = findFieldAny(findClass(classLoader, className), fieldNames);
        Object value = field.get(null);
        if (value == null || !value.getClass().isArray() || java.lang.reflect.Array.getLength(value) == 0) {
            return false;
        }
        return java.lang.reflect.Array.get(value, 0) != null;
    }

    private static boolean isStaticBooleanFlag(ClassLoader classLoader, String className, String... fieldNames) throws Exception {
        Field field = findFieldAny(findClass(classLoader, className), fieldNames);
        Object value = field.get(null);
        if (value instanceof boolean[]) {
            boolean[] flags = (boolean[]) value;
            return flags.length > 0 && flags[0];
        }
        if (value instanceof Boolean) {
            return (Boolean) value;
        }
        return false;
    }

    private static boolean booleanNoArg(Class<?> cls, String methodName) throws Exception {
        Object result = findNoArgMethod(cls, methodName).invoke(null);
        return result instanceof Boolean && (Boolean) result;
    }

    private static boolean bindRuntimeIdentity(BridgeConfig config) {
        String wxid = "";
        String nickname = "";
        Object db = LAST_DATABASE;
        if (db == null) {
            db = findContactDatabaseOnMainThread(config);
        }
        WeChatIdentity identity = readWeChatIdentity(db);
        wxid = identity.wxid;
        nickname = identity.nickname;
        if (isBlank(wxid)) {
            wxid = CURRENT_WXID;
        }
        if (isBlank(nickname)) {
            nickname = CURRENT_NICKNAME;
        }
        if (isBlank(wxid)) {
            log("runtime identity not ready: current wxid cannot be detected yet");
            return false;
        }
        if (!isBlank(CURRENT_WXID) && !wxid.equals(CURRENT_WXID)) {
            REGISTERED_KEY = "";
            REGISTERED_DEVICE = "";
            LAST_REGISTER_SUCCESS_AT = 0L;
            log("wechat identity changed wxid=" + wxid);
        }
        CURRENT_WXID = wxid;
        CURRENT_NICKNAME = nickname;
        config.selfWxid = wxid;
        config.nickname = isBlank(nickname) ? wxid : nickname;
        return true;
    }

    private static WeChatIdentity readWeChatIdentity(Object db) {
        if (db == null) {
            return new WeChatIdentity("", "");
        }
        String wxid = "";
        String nickname = "";
        String[][] candidates = new String[][]{
                {"SELECT value FROM userinfo WHERE id=2 LIMIT 1", ""},
                {"SELECT value FROM userinfo WHERE id=42 LIMIT 1", ""},
                {"SELECT value FROM userinfo WHERE id=4 LIMIT 1", "nickname"},
                {"SELECT value FROM userinfo WHERE id=5 LIMIT 1", "nickname"},
                {"SELECT value FROM userinfo WHERE id=6 LIMIT 1", "nickname"}
        };
        for (String[] candidate : candidates) {
            String value = readSingleString(db, candidate[0]);
            if (isBlank(value)) {
                continue;
            }
            if ("nickname".equals(candidate[1])) {
                if (isBlank(nickname)) {
                    nickname = value;
                }
                continue;
            }
            if (looksLikeWxid(value)) {
                wxid = value;
                break;
            }
        }
        return new WeChatIdentity(wxid, nickname);
    }

    private static String readSingleString(Object db, String sql) {
        Object cursor = null;
        try {
            cursor = rawQuery(db, sql, new String[]{});
            if (cursor == null) {
                return "";
            }
            Method moveToFirst = findNoArgMethod(cursor.getClass(), "moveToFirst");
            if (Boolean.TRUE.equals(moveToFirst.invoke(cursor))) {
                return stringColumn(cursor, 0);
            }
        } catch (Throwable ignored) {
            // WeChat database schemas vary between versions; try the next candidate.
        } finally {
            closeQuietly(cursor);
        }
        return "";
    }

    private static final class WeChatIdentity {
        final String wxid;
        final String nickname;

        WeChatIdentity(String wxid, String nickname) {
            this.wxid = wxid == null ? "" : wxid.trim();
            this.nickname = nickname == null ? "" : nickname.trim();
        }
    }

    private static boolean ensureRegistered(BridgeConfig config) throws Exception {
        if (isBlank(config.apiKey) || isBlank(config.selfWxid)) {
            return false;
        }
        String key = config.apiKey + "\n" + config.selfWxid + "\n" + config.signature;
        long now = System.currentTimeMillis();
        if (key.equals(REGISTERED_KEY) && !isBlank(REGISTERED_DEVICE)) {
            config.device = REGISTERED_DEVICE;
            if (now - LAST_REGISTER_SUCCESS_AT < 60000L) {
                return true;
            }
        }
        if (now - LAST_REGISTER_ATTEMPT_AT < 5000L) {
            return false;
        }
        LAST_REGISTER_ATTEMPT_AT = now;
        return registerModule(config, key);
    }

    private static boolean registerModule(BridgeConfig config, String key) throws Exception {
        if (isBlank(config.apiKey) || isBlank(config.selfWxid)) {
            return false;
        }
        String body = "{"
                + "\"api_key\":\"" + json(config.apiKey) + "\","
                + "\"device\":\"" + json(config.device) + "\","
                + "\"wxid\":\"" + json(config.selfWxid) + "\","
                + "\"nickname\":\"" + json(config.nickname) + "\""
                + "}";
        String response = postJson(config, "/module/register", body);
        JSONObject root = new JSONObject(response);
        JSONObject result = root.optJSONObject("result");
        String device = "";
        if (result != null) {
            JSONObject deviceObject = result.optJSONObject("device");
            if (deviceObject != null) {
                device = deviceObject.optString("name", "");
            }
            if (isBlank(device)) {
                JSONObject account = result.optJSONObject("account");
                if (account != null) {
                    device = account.optString("device", "");
                }
            }
        }
        if (isBlank(device)) {
            log("module register response missing server device");
            return false;
        }
        REGISTERED_KEY = key;
        REGISTERED_DEVICE = device;
        LAST_REGISTER_SUCCESS_AT = System.currentTimeMillis();
        config.device = device;
        log("module registered device=" + device + " wxid=" + config.selfWxid);
        return true;
    }

    private static void syncContactsIfDue(BridgeConfig config) {
        if (config.contactSyncIntervalMs <= 0L) {
            return;
        }
        long now = System.currentTimeMillis();
        if (now - LAST_CONTACT_SYNC_AT < config.contactSyncIntervalMs) {
            return;
        }
        Object db = LAST_DATABASE;
        if (db == null) {
            db = findContactDatabaseOnMainThread(config);
        }
        if (db == null) {
            if (now - LAST_CONTACT_SYNC_SKIP_LOG_AT > 60000L) {
                LAST_CONTACT_SYNC_SKIP_LOG_AT = now;
                log("contact sync skipped: WeChat database not captured yet");
            }
            return;
        }
        LAST_CONTACT_SYNC_AT = now;
        try {
            JSONArray contacts = readContacts(db, config);
            if (contacts.length() == 0) {
                log("contact sync skipped: no friend contacts read");
                return;
            }
            JSONObject body = new JSONObject();
            body.put("api_key", config.apiKey);
            body.put("device", config.device);
            body.put("wxid", config.selfWxid);
            body.put("complete", true);
            body.put("contacts", contacts);
            postJson(config, "/module/contacts/snapshot", body.toString());
            log("contact sync uploaded count=" + contacts.length()
                    + " includeChatrooms=" + config.includeChatrooms);
        } catch (Throwable t) {
            log("contact sync failed: " + shortError(t));
        }
    }

    private static void pollMessagesIfDue(BridgeConfig config) {
        long now = System.currentTimeMillis();
        if (now - LAST_MESSAGE_POLL_AT < Math.max(1000L, config.pollIntervalMs)) {
            return;
        }
        LAST_MESSAGE_POLL_AT = now;
        Object db = LAST_DATABASE;
        if (db == null) {
            db = findContactDatabaseOnMainThread(config);
        }
        if (db == null) {
            return;
        }
        try {
            if (!MESSAGE_WATERMARK_READY) {
                LAST_MESSAGE_ID = readMaxMessageId(db);
                MESSAGE_WATERMARK_READY = true;
                log("message poll watermark initialized msgId=" + LAST_MESSAGE_ID);
                return;
            }
            int count = pollNewMessages(db, config);
            if (count > 0) {
                log("message poll uploaded count=" + count + " lastMsgId=" + LAST_MESSAGE_ID);
            }
        } catch (Throwable t) {
            log("message poll failed: " + shortError(t));
        }
    }

    private static long readMaxMessageId(Object db) throws Exception {
        Object cursor = rawQuery(db, "SELECT MAX(msgId) FROM message", new String[]{});
        if (cursor == null) {
            return 0L;
        }
        try {
            Method moveToFirst = findNoArgMethod(cursor.getClass(), "moveToFirst");
            if (Boolean.TRUE.equals(moveToFirst.invoke(cursor))) {
                return longColumn(cursor, 0);
            }
            return 0L;
        } finally {
            closeQuietly(cursor);
        }
    }

    private static int pollNewMessages(Object db, BridgeConfig config) throws Exception {
        boolean hasMediaHint = true;
        Object cursor;
        try {
            cursor = rawQuery(db, ""
                    + "SELECT msgId,talker,COALESCE(content,''),isSend,createTime,type,COALESCE(imgPath,'') "
                    + "FROM message "
                    + "WHERE msgId > ? AND talker IS NOT NULL AND talker <> '' "
                    + "ORDER BY msgId ASC "
                    + "LIMIT 50", new String[]{String.valueOf(LAST_MESSAGE_ID)});
        } catch (Throwable t) {
            hasMediaHint = false;
            cursor = rawQuery(db, ""
                    + "SELECT msgId,talker,COALESCE(content,''),isSend,createTime,type "
                    + "FROM message "
                    + "WHERE msgId > ? AND talker IS NOT NULL AND talker <> '' "
                    + "ORDER BY msgId ASC "
                    + "LIMIT 50", new String[]{String.valueOf(LAST_MESSAGE_ID)});
        }
        if (cursor == null) {
            return 0;
        }
        int count = 0;
        try {
            Method moveToNext = findNoArgMethod(cursor.getClass(), "moveToNext");
            while (Boolean.TRUE.equals(moveToNext.invoke(cursor))) {
                long msgId = longColumn(cursor, 0);
                if (msgId <= LAST_MESSAGE_ID) {
                    continue;
                }
                LAST_MESSAGE_ID = msgId;

                String talker = stringColumn(cursor, 1);
                String content = stringColumn(cursor, 2);
                int isSend = intColumn(cursor, 3);
                long createTime = normalizeCreateTime(longColumn(cursor, 4));
                int type = intColumn(cursor, 5);
                String imgPath = hasMediaHint ? stringColumn(cursor, 6) : "";
                if (!shouldReportMessage(talker, content, type)) {
                    continue;
                }

                MessagePayload payload = buildMessagePayload(config, talker, content, isSend == 1 ? Integer.valueOf(1) : Integer.valueOf(0), msgId, createTime, type <= 0 ? 1 : type, imgPath);
                post(config, payload);
                count++;
            }
        } finally {
            closeQuietly(cursor);
        }
        return count;
    }

    private static boolean shouldReportMessage(String talker, String content, int type) {
        if (isBlank(talker)) {
            return false;
        }
        String normalizedTalker = talker.trim().toLowerCase(Locale.US);
        if (normalizedTalker.startsWith("gh_") || isSystemContact(normalizedTalker)) {
            return false;
        }
        if (type == 1) {
            if (isBlank(content)) {
                return false;
            }
            String text = normalizeMessageText(talker, content);
            return !looksLikeXmlPayload(text);
        }
        return type > 0;
    }

    private static MessagePayload buildMessagePayload(BridgeConfig config, String talker, String content, Integer isSend, Long msgId, Long createTime, int type, String mediaHint) {
        MessagePayload payload = new MessagePayload();
        String normalizedTalker = talker == null ? "" : talker.trim();
        boolean chatroom = isChatroomTalker(normalizedTalker);
        String text = displayMessageText(normalizedTalker, content, type);
        payload.id = msgId == null || msgId <= 0 ? "" : String.valueOf(msgId);
        payload.eventId = msgId == null ? 0L : msgId;
        payload.chatRecordId = msgId == null ? 0L : msgId;
        payload.apiKey = config.apiKey;
        payload.device = config.device;
        payload.chatId = normalizedTalker;
        payload.chatKind = chatroom ? "room" : "direct";
        payload.roomId = chatroom ? normalizedTalker : "";
        payload.text = text;
        payload.messageType = type;
        payload.createTime = normalizeCreateTime(createTime);
        attachMedia(config, payload, type, mediaHint);

        boolean sent = isSend != null && isSend == 1;
        if (sent) {
            payload.direction = "sent";
            payload.from = config.selfWxid;
            payload.to = normalizedTalker;
            payload.sender = chatroom ? config.selfWxid : "";
        } else {
            payload.direction = "recv";
            payload.from = normalizedTalker;
            payload.to = config.selfWxid;
            payload.sender = chatroom ? extractChatroomSender(content) : "";
        }
        return payload;
    }

    private static void attachMedia(BridgeConfig config, MessagePayload payload, int type, String mediaHint) {
        String kind = mediaKindForMessageType(type);
        if (isBlank(kind)) {
            return;
        }
        payload.mediaKind = kind;
        if (!config.mediaUploadEnabled) {
            return;
        }
        File file = resolveMediaFile(type, mediaHint);
        if (file == null || !file.isFile()) {
            return;
        }
        long length = file.length();
        if (length <= 0 || length > config.mediaUploadLimitBytes) {
            log("skip media upload type=" + type + " size=" + length + " path=" + file.getAbsolutePath());
            return;
        }
        try {
            byte[] bytes = readFileBytes(file, config.mediaUploadLimitBytes);
            String mime = detectMediaMime(type, file, bytes);
            payload.mediaMime = mime;
            payload.mediaName = mediaUploadName(file, mime, payload.id);
            payload.mediaSize = bytes.length;
            payload.mediaBase64 = Base64.encodeToString(bytes, Base64.NO_WRAP);
        } catch (Throwable t) {
            log("read media failed type=" + type + " hint=" + mediaHint + " error=" + shortError(t));
        }
    }

    private static String mediaKindForMessageType(int type) {
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

    private static File resolveMediaFile(int type, String mediaHint) {
        List<String> names = mediaCandidateNames(type, mediaHint);
        if (names.isEmpty()) {
            return null;
        }
        File direct = directMediaFile(mediaHint);
        if (direct != null) {
            return direct;
        }
        Context context = APP_CONTEXT;
        if (context == null) {
            Object app = currentApplication();
            if (app instanceof Context) {
                context = (Context) app;
            }
        }
        if (context == null) {
            return null;
        }
        File appRoot = context.getFilesDir() == null ? null : context.getFilesDir().getParentFile();
        if (appRoot == null) {
            return null;
        }
        File microMsg = new File(appRoot, "MicroMsg");
        File[] profiles = microMsg.listFiles();
        if (profiles == null) {
            return null;
        }
        String[] roots = mediaSearchRoots(type);
        for (File profile : profiles) {
            if (profile == null || !profile.isDirectory()) {
                continue;
            }
            for (String rootName : roots) {
                File found = findNamedFile(new File(profile, rootName), names, 5, new int[]{0});
                if (found != null) {
                    return found;
                }
            }
        }
        return null;
    }

    private static File directMediaFile(String mediaHint) {
        if (isBlank(mediaHint)) {
            return null;
        }
        String[] candidates = new String[]{mediaHint.trim(), normalizeMediaHint(mediaHint)};
        Context context = APP_CONTEXT;
        File appRoot = null;
        if (context != null && context.getFilesDir() != null) {
            appRoot = context.getFilesDir().getParentFile();
        }
        for (String candidate : candidates) {
            if (isBlank(candidate)) {
                continue;
            }
            File direct = new File(candidate);
            if (direct.isFile()) {
                return direct;
            }
            if (appRoot != null) {
                File relative = new File(appRoot, candidate);
                if (relative.isFile()) {
                    return relative;
                }
            }
        }
        return null;
    }

    private static List<String> mediaCandidateNames(int type, String mediaHint) {
        List<String> names = new ArrayList<>();
        String normalized = normalizeMediaHint(mediaHint);
        addMediaCandidate(names, normalized);
        int slash = Math.max(normalized.lastIndexOf('/'), normalized.lastIndexOf(File.separatorChar));
        if (slash >= 0 && slash + 1 < normalized.length()) {
            addMediaCandidate(names, normalized.substring(slash + 1));
        }
        List<String> snapshot = new ArrayList<>(names);
        for (String candidate : snapshot) {
            if (type == 3 && candidate.startsWith("th_") && candidate.length() > 3) {
                addMediaCandidate(names, candidate.substring(3));
            }
            if (type == 34 && candidate.indexOf('.') < 0) {
                addMediaCandidate(names, candidate + ".amr");
                addMediaCandidate(names, candidate + ".silk");
            }
        }
        return names;
    }

    private static String normalizeMediaHint(String mediaHint) {
        if (isBlank(mediaHint)) {
            return "";
        }
        String value = mediaHint.trim();
        int query = value.indexOf('?');
        if (query >= 0) {
            value = value.substring(0, query);
        }
        int scheme = value.indexOf("://");
        if (scheme >= 0 && scheme + 3 < value.length()) {
            value = value.substring(scheme + 3);
        }
        return value.trim();
    }

    private static void addMediaCandidate(List<String> names, String name) {
        if (isBlank(name)) {
            return;
        }
        String value = name.trim();
        for (String existing : names) {
            if (existing.equals(value)) {
                return;
            }
        }
        names.add(value);
    }

    private static String[] mediaSearchRoots(int type) {
        switch (type) {
            case 3:
                return new String[]{"image2"};
            case 34:
                return new String[]{"voice2"};
            case 43:
            case 62:
                return new String[]{"video"};
            case 49:
                return new String[]{"attachment", "openapi", "image2"};
            default:
                return new String[]{"image2", "voice2", "video", "attachment"};
        }
    }

    private static File findNamedFile(File root, List<String> names, int depth, int[] visited) {
        if (root == null || !root.isDirectory() || depth < 0 || visited[0] > 6000) {
            return null;
        }
        File[] files = root.listFiles();
        if (files == null) {
            return null;
        }
        for (File file : files) {
            if (file == null) {
                continue;
            }
            visited[0]++;
            if (file.isFile() && mediaNameMatches(file.getName(), names)) {
                return file;
            }
        }
        for (File file : files) {
            if (file != null && file.isDirectory()) {
                File found = findNamedFile(file, names, depth - 1, visited);
                if (found != null) {
                    return found;
                }
            }
        }
        return null;
    }

    private static boolean mediaNameMatches(String fileName, List<String> names) {
        if (isBlank(fileName)) {
            return false;
        }
        for (String name : names) {
            if (fileName.equals(name)) {
                return true;
            }
        }
        return false;
    }

    private static byte[] readFileBytes(File file, long limit) throws IOException {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buffer = new byte[8192];
        long total = 0L;
        try (FileInputStream input = new FileInputStream(file)) {
            int read;
            while ((read = input.read(buffer)) != -1) {
                total += read;
                if (total > limit) {
                    throw new IOException("media file exceeds limit");
                }
                out.write(buffer, 0, read);
            }
        }
        return out.toByteArray();
    }

    private static String detectMediaMime(int type, File file, byte[] bytes) {
        String name = file == null ? "" : file.getName().toLowerCase(Locale.US);
        if (name.endsWith(".jpg") || name.endsWith(".jpeg") || startsWith(bytes, new byte[]{(byte) 0xff, (byte) 0xd8})) {
            return "image/jpeg";
        }
        if (name.endsWith(".png") || startsWith(bytes, new byte[]{(byte) 0x89, 0x50, 0x4e, 0x47})) {
            return "image/png";
        }
        if (name.endsWith(".gif") || startsWith(bytes, new byte[]{0x47, 0x49, 0x46})) {
            return "image/gif";
        }
        if (name.endsWith(".webp") || containsAsciiAt(bytes, "WEBP", 8)) {
            return "image/webp";
        }
        if (name.endsWith(".mp4") || containsAsciiAt(bytes, "ftyp", 4)) {
            return "video/mp4";
        }
        if (name.endsWith(".amr") || startsWith(bytes, "#!AMR".getBytes(StandardCharsets.US_ASCII))) {
            return "audio/amr";
        }
        if (type == 34) {
            return "audio/amr";
        }
        if (type == 3) {
            return "image/jpeg";
        }
        if (type == 43 || type == 62) {
            return "video/mp4";
        }
        return "application/octet-stream";
    }

    private static boolean startsWith(byte[] bytes, byte[] prefix) {
        if (bytes == null || prefix == null || bytes.length < prefix.length) {
            return false;
        }
        for (int i = 0; i < prefix.length; i++) {
            if (bytes[i] != prefix[i]) {
                return false;
            }
        }
        return true;
    }

    private static boolean containsAsciiAt(byte[] bytes, String text, int offset) {
        if (bytes == null || text == null || offset < 0 || bytes.length < offset + text.length()) {
            return false;
        }
        byte[] expected = text.getBytes(StandardCharsets.US_ASCII);
        for (int i = 0; i < expected.length; i++) {
            if (bytes[offset + i] != expected[i]) {
                return false;
            }
        }
        return true;
    }

    private static String mediaUploadName(File file, String mime, String id) {
        String name = file == null ? "" : file.getName();
        if (isBlank(name)) {
            name = isBlank(id) ? "media" : "media-" + id;
        }
        if (name.indexOf('.') >= 0) {
            return name;
        }
        return name + extensionForMime(mime);
    }

    private static String extensionForMime(String mime) {
        if ("image/png".equals(mime)) {
            return ".png";
        }
        if ("image/gif".equals(mime)) {
            return ".gif";
        }
        if ("image/webp".equals(mime)) {
            return ".webp";
        }
        if ("audio/amr".equals(mime)) {
            return ".amr";
        }
        if ("video/mp4".equals(mime)) {
            return ".mp4";
        }
        if ("image/jpeg".equals(mime)) {
            return ".jpg";
        }
        return ".bin";
    }

    private static String displayMessageText(String talker, String content, int type) {
        if (type == 1) {
            return normalizeMessageText(talker, content);
        }
        String label = messageTypeLabel(type);
        String detail = nonTextMessageDetail(type, normalizeMessageText(talker, content));
        if (isBlank(detail)) {
            return label;
        }
        return label + " " + detail;
    }

    private static String nonTextMessageDetail(int type, String content) {
        String detail = firstNonBlank(
                extractXmlField(content, "title"),
                extractXmlField(content, "des"),
                extractXmlField(content, "filename"),
                extractXmlField(content, "appname"),
                extractXmlField(content, "displayname"));
        if (!isBlank(detail)) {
            return compactDisplayText(detail, 80);
        }
        if (!looksLikeXmlPayload(content)) {
            return compactDisplayText(content, 80);
        }
        if (type == 10000) {
            return "";
        }
        return "";
    }

    private static String messageTypeLabel(int type) {
        switch (type) {
            case 1:
                return "[文本]";
            case 3:
                return "[图片]";
            case 34:
                return "[语音]";
            case 37:
                return "[好友请求]";
            case 43:
                return "[视频]";
            case 47:
                return "[表情]";
            case 48:
                return "[位置]";
            case 49:
                return "[链接/文件]";
            case 50:
                return "[通话]";
            case 62:
                return "[小视频]";
            case 10000:
                return "[系统消息]";
            default:
                return "[消息 " + type + "]";
        }
    }

    private static boolean looksLikeXmlPayload(String value) {
        if (isBlank(value)) {
            return false;
        }
        String text = value.trim().toLowerCase(Locale.US);
        return text.startsWith("<msg")
                || text.startsWith("<?xml")
                || text.startsWith("<sysmsg")
                || text.startsWith("<appmsg")
                || text.startsWith("<template>");
    }

    private static String extractXmlField(String xml, String field) {
        if (isBlank(xml) || isBlank(field)) {
            return "";
        }
        String text = xml.trim();
        String lower = text.toLowerCase(Locale.US);
        String openTag = "<" + field.toLowerCase(Locale.US) + ">";
        int start = lower.indexOf(openTag);
        if (start >= 0) {
            start += openTag.length();
        } else {
            String prefix = "<" + field.toLowerCase(Locale.US);
            start = lower.indexOf(prefix);
            if (start < 0) {
                return "";
            }
            start = lower.indexOf(">", start);
            if (start < 0) {
                return "";
            }
            start++;
        }
        String closeTag = "</" + field.toLowerCase(Locale.US) + ">";
        int end = lower.indexOf(closeTag, start);
        if (end <= start) {
            return "";
        }
        return cleanDisplayText(text.substring(start, end), 120);
    }

    private static String cleanDisplayText(String text, int maxLen) {
        if (isBlank(text)) {
            return "";
        }
        String value = text.trim()
                .replace("\r", " ")
                .replace("\n", " ")
                .replace("\t", " ");
        if (value.startsWith("<![CDATA[") && value.endsWith("]]>") && value.length() >= 12) {
            value = value.substring(9, value.length() - 3).trim();
        }
        value = value.replace("&lt;", "<")
                .replace("&gt;", ">")
                .replace("&amp;", "&")
                .replace("&quot;", "\"")
                .replace("&#39;", "'");
        while (value.contains("  ")) {
            value = value.replace("  ", " ");
        }
        if (maxLen > 0 && value.length() > maxLen) {
            value = value.substring(0, maxLen).trim();
        }
        return value;
    }

    private static String compactDisplayText(String text, int maxLen) {
        return cleanDisplayText(text, maxLen);
    }

    private static String firstNonBlank(String... values) {
        if (values == null) {
            return "";
        }
        for (String value : values) {
            if (!isBlank(value)) {
                return value;
            }
        }
        return "";
    }

    private static boolean isChatroomTalker(String talker) {
        return !isBlank(talker) && talker.toLowerCase(Locale.US).endsWith("@chatroom");
    }

    private static String normalizeMessageText(String talker, String content) {
        if (isBlank(content)) {
            return "";
        }
        String text = content.trim();
        if (!isChatroomTalker(talker)) {
            return text;
        }
        int newline = text.indexOf(":\n");
        if (newline <= 0) {
            return text;
        }
        String prefix = text.substring(0, newline).trim();
        String body = text.substring(newline + 2).trim();
        if (looksLikeWxid(prefix) && !isBlank(body)) {
            return body;
        }
        return text;
    }

    private static String extractChatroomSender(String content) {
        if (isBlank(content)) {
            return "";
        }
        String text = content.trim();
        int newline = text.indexOf(":\n");
        if (newline <= 0) {
            return "";
        }
        String prefix = text.substring(0, newline).trim();
        return looksLikeWxid(prefix) ? prefix : "";
    }

    private static boolean looksLikeWxid(String value) {
        if (isBlank(value)) {
            return false;
        }
        String normalized = value.trim();
        if (normalized.length() < 3 || normalized.indexOf(' ') >= 0 || normalized.indexOf('\n') >= 0 || normalized.indexOf('\r') >= 0) {
            return false;
        }
        return normalized.startsWith("wxid_")
                || normalized.startsWith("gh_")
                || normalized.contains("@chatroom")
                || normalized.contains("_");
    }

    private static JSONArray readContacts(Object db, BridgeConfig config) throws Exception {
        int limit = ContactSnapshotPlan.outputLimit(config.contactSyncLimit);
        Object cursor = rawQuery(db, ContactSnapshotPlan.CONTACT_QUERY, new String[0]);
        JSONArray out = new JSONArray();
        Set<String> seen = new HashSet<>();
        if (cursor == null) {
            return out;
        }
        try {
            Method moveToNext = findNoArgMethod(cursor.getClass(), "moveToNext");
            while (out.length() < limit && Boolean.TRUE.equals(moveToNext.invoke(cursor))) {
                String wxid = stringColumn(cursor, 0);
                int type = intColumn(cursor, 4);
                boolean chatroom = wxid.toLowerCase(Locale.US).endsWith("@chatroom");
                if (!shouldIncludeContact(wxid, type, chatroom, config)) {
                    continue;
                }
                String normalized = wxid.trim().toLowerCase(Locale.US);
                if (!seen.add(normalized)) {
                    continue;
                }
                JSONObject contact = new JSONObject();
                contact.put("wxid", wxid);
                contact.put("nickname", contactNickname(wxid, stringColumn(cursor, 1)));
                contact.put("remark", stringColumn(cursor, 2));
                contact.put("alias", stringColumn(cursor, 3));
                contact.put("type", type);
                contact.put("verify_flag", intColumn(cursor, 5));
                contact.put("chatroom", chatroom);
                contact.put("deleted", false);
                out.put(contact);
            }
        } finally {
            closeQuietly(cursor);
        }
        if (config.includeChatrooms && out.length() < limit) {
            int added = appendChatroomContacts(db, out, seen, limit - out.length());
            if (added == 0 && out.length() < limit) {
                appendActiveDatabaseChatrooms(out, seen, limit - out.length());
            }
        }
        return out;
    }

    private static int appendChatroomContacts(Object db, JSONArray out, Set<String> seen, int limit) {
        if (limit <= 0) {
            return 0;
        }
        Object cursor = null;
        try {
            cursor = rawQuery(db, ""
                    + "SELECT chatroomname,displayname "
                    + "FROM chatroom "
                    + "WHERE chatroomname IS NOT NULL AND chatroomname <> '' "
                    + "LIMIT ?", new String[]{String.valueOf(limit)});
        } catch (Throwable first) {
            try {
                cursor = rawQuery(db, ""
                        + "SELECT chatroomname,'' "
                        + "FROM chatroom "
                        + "WHERE chatroomname IS NOT NULL AND chatroomname <> '' "
                        + "LIMIT ?", new String[]{String.valueOf(limit)});
            } catch (Throwable second) {
                log("chatroom contact scan skipped: " + shortError(second));
                return 0;
            }
        }
        if (cursor == null) {
            return 0;
        }
        int added = 0;
        try {
            Method moveToNext = findNoArgMethod(cursor.getClass(), "moveToNext");
            while (Boolean.TRUE.equals(moveToNext.invoke(cursor))) {
                String wxid = stringColumn(cursor, 0);
                if (isBlank(wxid) || !wxid.toLowerCase(Locale.US).endsWith("@chatroom")) {
                    continue;
                }
                String normalized = wxid.trim().toLowerCase(Locale.US);
                if (!seen.add(normalized)) {
                    continue;
                }
                String nickname = stringColumn(cursor, 1);
                JSONObject contact = new JSONObject();
                contact.put("wxid", wxid);
                contact.put("nickname", isBlank(nickname) ? wxid : nickname);
                contact.put("remark", "");
                contact.put("alias", "");
                contact.put("type", 0);
                contact.put("verify_flag", 0);
                contact.put("chatroom", true);
                contact.put("deleted", false);
                out.put(contact);
                added++;
            }
        } catch (Throwable t) {
            log("chatroom contact scan failed: " + shortError(t));
        } finally {
            closeQuietly(cursor);
        }
        if (added > 0) {
            log("chatroom contact scan added=" + added);
        }
        return added;
    }

    private static void appendActiveDatabaseChatrooms(JSONArray out, Set<String> seen, int limit) {
        if (limit <= 0) {
            return;
        }
        try {
            ClassLoader classLoader = WECHAT_CLASS_LOADER;
            if (classLoader == null) {
                classLoader = runtimeClassLoader(HookEntry.class.getClassLoader());
            } else {
                classLoader = runtimeClassLoader(classLoader);
            }
            Class<?> dbClass = findClass(classLoader, "com.tencent.wcdb.database.SQLiteDatabase");
            Field activeField = findField(dbClass, "sActiveDatabases");
            Object active = activeField.get(null);
            if (!(active instanceof Map)) {
                return;
            }
            Object[] databases;
            synchronized (active) {
                databases = ((Map<?, ?>) active).keySet().toArray();
            }
            int total = 0;
            for (Object candidate : databases) {
                if (candidate == null || out.length() >= limit) {
                    continue;
                }
                int added = appendChatroomContacts(candidate, out, seen, limit - out.length());
                if (added > 0) {
                    total += added;
                    log("chatroom active database path=" + databasePath(candidate) + " added=" + added);
                }
            }
            if (total == 0) {
                log("chatroom active database scan found no rows dbCount=" + databases.length);
            }
        } catch (Throwable t) {
            log("chatroom active database scan failed: " + shortError(t));
        }
    }

    private static Object findContactDatabase(BridgeConfig config) {
        try {
            ClassLoader classLoader = WECHAT_CLASS_LOADER;
            if (classLoader == null) {
                classLoader = runtimeClassLoader(HookEntry.class.getClassLoader());
            } else {
                classLoader = runtimeClassLoader(classLoader);
            }
            Class<?> dbClass = findClass(classLoader, "com.tencent.wcdb.database.SQLiteDatabase");
            Field activeField = findField(dbClass, "sActiveDatabases");
            Object active = activeField.get(null);
            if (!(active instanceof Map)) {
                return null;
            }
            Object[] databases;
            synchronized (active) {
                databases = ((Map<?, ?>) active).keySet().toArray();
            }
            boolean verboseScanLog = shouldLogContactScan();
            if (verboseScanLog) {
                log("contact database scan active db count=" + databases.length);
            }
            for (Object db : databases) {
                if (db == null) {
                    continue;
                }
                try {
                    JSONArray contacts = readContacts(db, config);
                    if (contacts.length() > 0) {
                        LAST_DATABASE = db;
                        log("captured WeChat database from active set path=" + databasePath(db)
                                + " contacts=" + contacts.length());
                        return db;
                    }
                    if (verboseScanLog) {
                        log("contact database candidate empty path=" + databasePath(db));
                    }
                } catch (Throwable ignored) {
                    if (verboseScanLog) {
                        log("contact database candidate failed path=" + databasePath(db)
                                + " error=" + shortError(ignored));
                    }
                }
            }
        } catch (Throwable t) {
            log("contact database scan failed: " + shortError(t));
        }
        return null;
    }

    private static boolean shouldLogContactScan() {
        long now = System.currentTimeMillis();
        if (now - LAST_CONTACT_SCAN_LOG_AT < 60000L) {
            return false;
        }
        LAST_CONTACT_SCAN_LOG_AT = now;
        return true;
    }

    private static Object findContactDatabaseOnMainThread(final BridgeConfig config) {
        if (Looper.myLooper() == Looper.getMainLooper()) {
            return findContactDatabase(config);
        }
        FutureTask<Object> task = new FutureTask<>(new Callable<Object>() {
            @Override
            public Object call() {
                return findContactDatabase(config);
            }
        });
        new Handler(Looper.getMainLooper()).post(task);
        try {
            return task.get();
        } catch (Throwable t) {
            log("contact database main-thread scan failed: " + shortError(t));
            return null;
        }
    }

    private static String databasePath(Object db) {
        try {
            Object value = findNoArgMethod(db.getClass(), "getPath").invoke(db);
            return value == null ? "<unknown>" : String.valueOf(value);
        } catch (Throwable ignored) {
            return "<unknown>";
        }
    }

    private static Object rawQuery(Object db, String sql, String[] args) throws Exception {
        try {
            Method rawQuery = findMethod(db.getClass(), "rawQuery", String.class, Object[].class);
            return rawQuery.invoke(db, sql, (Object) args);
        } catch (NoSuchMethodException ignored) {
            try {
                Method rawQuery = findMethod(db.getClass(), "rawQuery", String.class, Object[].class, findClass(db.getClass().getClassLoader(), "com.tencent.wcdb.support.CancellationSignal"));
                return rawQuery.invoke(db, sql, (Object) args, null);
            } catch (NoSuchMethodException ignoredObjectOverload) {
                try {
                    Method rawQuery = findMethod(db.getClass(), "rawQuery", String.class, String[].class);
                    return rawQuery.invoke(db, sql, (Object) args);
                } catch (NoSuchMethodException ignoredStringOverload) {
                    Method rawQuery = findMethod(db.getClass(), "rawQuery", String.class, String[].class, int.class);
                    return rawQuery.invoke(db, sql, (Object) args, 0);
                }
            }
        }
    }

    private static boolean shouldIncludeContact(String wxid, int type, boolean chatroom, BridgeConfig config) {
        if (isBlank(wxid)) {
            return false;
        }
        String normalized = wxid.trim().toLowerCase(Locale.US);
        if (!isBlank(config.selfWxid) && normalized.equals(config.selfWxid.trim().toLowerCase(Locale.US))) {
            return false;
        }
        if (isFileHelperContact(normalized)) {
            return true;
        }
        if (chatroom) {
            return config.includeChatrooms;
        }
        if ((type & 1) == 0) {
            return false;
        }
        if (normalized.startsWith("gh_")) {
            return false;
        }
        return !isSystemContact(normalized);
    }

    private static String contactNickname(String wxid, String nickname) {
        if (isFileHelperContact(wxid) && isBlank(nickname)) {
            return "文件传输助手";
        }
        return nickname;
    }

    private static boolean isFileHelperContact(String wxid) {
        return "filehelper".equals(wxid == null ? "" : wxid.trim().toLowerCase(Locale.US));
    }

    private static boolean isSystemContact(String wxid) {
        return "weixin".equals(wxid)
                || "medianote".equals(wxid)
                || "fmessage".equals(wxid)
                || "qmessage".equals(wxid)
                || "tmessage".equals(wxid)
                || "qqmail".equals(wxid)
                || "qqsync".equals(wxid)
                || "lbsapp".equals(wxid)
                || "shakeapp".equals(wxid)
                || "feedsapp".equals(wxid)
                || "masssendapp".equals(wxid)
                || "newsapp".equals(wxid)
                || "blogapp".equals(wxid)
                || "officialaccounts".equals(wxid)
                || "helper_entry".equals(wxid)
                || "voiceinputapp".equals(wxid)
                || "voicevoipapp".equals(wxid)
                || "weixinreminder".equals(wxid)
                || "notifymessage".equals(wxid);
    }

    private static String stringColumn(Object cursor, int index) {
        try {
            Object value = findMethod(cursor.getClass(), "getString", int.class).invoke(cursor, index);
            return value == null ? "" : String.valueOf(value);
        } catch (Throwable t) {
            return "";
        }
    }

    private static int intColumn(Object cursor, int index) {
        try {
            Object value = findMethod(cursor.getClass(), "getInt", int.class).invoke(cursor, index);
            if (value instanceof Number) {
                return ((Number) value).intValue();
            }
            return Integer.parseInt(String.valueOf(value));
        } catch (Throwable t) {
            return 0;
        }
    }

    private static long longColumn(Object cursor, int index) {
        try {
            Object value = findMethod(cursor.getClass(), "getLong", int.class).invoke(cursor, index);
            if (value instanceof Number) {
                return ((Number) value).longValue();
            }
            return Long.parseLong(String.valueOf(value));
        } catch (Throwable t) {
            return 0L;
        }
    }

    private static void closeQuietly(Object closeable) {
        if (closeable == null) {
            return;
        }
        try {
            findNoArgMethod(closeable.getClass(), "close").invoke(closeable);
        } catch (Throwable ignored) {
            // Ignore close failures from WeChat cursor implementations.
        }
    }

    private static void pollOutbox(BridgeConfig config, ClassLoader classLoader) throws Exception {
        String body = "{"
                + "\"api_key\":\"" + json(config.apiKey) + "\","
                + "\"device\":\"" + json(config.device) + "\","
                + "\"wxid\":\"" + json(config.selfWxid) + "\","
                + "\"limit\":" + config.pollLimit
                + "}";
        String response = postJson(config, "/module/outbox/poll", body);
        JSONObject root = new JSONObject(response);
        JSONArray items = root.optJSONArray("items");
        if (items == null || items.length() == 0) {
            return;
        }

        JSONArray ackItems = handleOutboxItems(items, classLoader);
        if (ackItems.length() == 0) {
            return;
        }
        JSONObject ackBody = new JSONObject();
        ackBody.put("api_key", config.apiKey);
        ackBody.put("device", config.device);
        ackBody.put("wxid", config.selfWxid);
        ackBody.put("items", ackItems);
        postJson(config, "/module/outbox/ack", ackBody.toString());
    }

    private static boolean runOutboxWebSocket(BridgeConfig config, ClassLoader classLoader) {
        if (!supportsWebSocket(config)) {
            return false;
        }
        try {
            websocketLoop(config, classLoader);
        } catch (Throwable t) {
            logWebSocketFailure("outbox websocket unavailable: " + shortError(t));
        }
        return false;
    }

    private static void websocketLoop(BridgeConfig config, ClassLoader classLoader) throws Exception {
        GatewayEndpoint endpoint = GatewayEndpoint.parse(config.baseUrl);
        String host = endpoint.host();
        String hostHeader = endpoint.hostHeader();
        String path = endpoint.requestPath("/module/outbox/ws")
                + "?api_key=" + urlEncode(config.apiKey)
                + "&device=" + urlEncode(config.device)
                + "&wxid=" + urlEncode(config.selfWxid);
        byte[] nonce = new byte[16];
        new SecureRandom().nextBytes(nonce);
        String key = Base64.encodeToString(nonce, Base64.NO_WRAP);

        try (Socket socket = openGatewaySocket(host, endpoint.port(), endpoint.isTls())) {
            socket.setSoTimeout(30000);
            InputStream input = socket.getInputStream();
            OutputStream output = socket.getOutputStream();
            String request = "GET " + path + " HTTP/1.1\r\n"
                    + "Host: " + hostHeader + "\r\n"
                    + "Upgrade: websocket\r\n"
                    + "Connection: Upgrade\r\n"
                    + "Sec-WebSocket-Version: 13\r\n"
                    + "Sec-WebSocket-Key: " + key + "\r\n"
                    + "\r\n";
            output.write(request.getBytes(StandardCharsets.ISO_8859_1));
            output.flush();
            verifyWebSocketHandshake(input, key);
            log("outbox websocket connected host=" + hostHeader + " device=" + config.device);
            writeWebSocketText(output, "{\"type\":\"poll\"}");

            while (true) {
                if (configChanged(config)) {
                    log("outbox websocket config changed; reconnect");
                    return;
                }
                WebSocketFrame frame = readWebSocketFrame(input);
                if (frame.opcode == 0x8) {
                    return;
                }
                if (frame.opcode == 0x9) {
                    writeWebSocketFrame(output, 0xA, frame.payload);
                    continue;
                }
                if (frame.opcode == 0xA) {
                    continue;
                }
                if (frame.opcode != 0x1) {
                    continue;
                }
                JSONObject root = new JSONObject(new String(frame.payload, StandardCharsets.UTF_8));
                String type = root.optString("type", "");
                if ("outbox".equals(type)) {
                    JSONArray ackItems = handleOutboxItems(root.optJSONArray("items"), classLoader);
                    if (ackItems.length() > 0) {
                        JSONObject ackBody = new JSONObject();
                        ackBody.put("type", "ack");
                        JSONObject ack = new JSONObject();
                        ack.put("api_key", config.apiKey);
                        ack.put("device", config.device);
                        ack.put("wxid", config.selfWxid);
                        ack.put("items", ackItems);
                        ackBody.put("ack", ack);
                        writeWebSocketText(output, ackBody.toString());
                    }
                } else if ("ready".equals(type)) {
                    log("outbox websocket ready");
                } else if ("error".equals(type)) {
                    log("outbox websocket server error: " + root.optString("error", ""));
                }
            }
        }
    }

    private static boolean configChanged(BridgeConfig config) {
        BridgeConfig latest = BridgeConfig.load(bridgeContext());
        return !String.valueOf(config.signature).equals(String.valueOf(latest.signature));
    }

    private static JSONArray handleOutboxItems(JSONArray items, ClassLoader classLoader) throws Exception {
        JSONArray ackItems = new JSONArray();
        if (items == null) {
            return ackItems;
        }
        for (int i = 0; i < items.length(); i++) {
            JSONObject item = items.getJSONObject(i);
            long id = item.optLong("id", 0);
            String wxid = item.optString("wxid", "");
            String text = item.optString("text", "");
            if (id <= 0 || isBlank(wxid) || isBlank(text)) {
                continue;
            }
            SendResult result = sendText(classLoader, wxid, text);
            JSONObject ack = new JSONObject();
            ack.put("id", id);
            ack.put("status", result.ok ? "sent" : "failed");
            if (result.chatRecordId > 0) {
                ack.put("chat_record_id", result.chatRecordId);
            }
            if (!result.ok) {
                ack.put("error", result.error);
            }
            ackItems.put(ack);
        }
        return ackItems;
    }

    private static void verifyWebSocketHandshake(InputStream input, String key) throws Exception {
        String headers = readHttpHeaders(input);
        String[] lines = headers.split("\r\n");
        if (lines.length == 0 || !lines[0].contains("101")) {
            throw new IOException("websocket upgrade failed: " + (lines.length == 0 ? "" : lines[0]));
        }
        String accept = "";
        for (String line : lines) {
            int index = line.indexOf(':');
            if (index <= 0) {
                continue;
            }
            String name = line.substring(0, index).trim();
            if ("Sec-WebSocket-Accept".equalsIgnoreCase(name)) {
                accept = line.substring(index + 1).trim();
            }
        }
        if (!webSocketAccept(key).equals(accept)) {
            throw new IOException("websocket accept mismatch");
        }
    }

    private static String readHttpHeaders(InputStream input) throws Exception {
        ByteArrayOutputStream buffer = new ByteArrayOutputStream();
        int matched = 0;
        int value;
        byte[] end = new byte[]{'\r', '\n', '\r', '\n'};
        while ((value = input.read()) != -1) {
            buffer.write(value);
            if ((byte) value == end[matched]) {
                matched++;
                if (matched == end.length) {
                    break;
                }
            } else {
                matched = (byte) value == end[0] ? 1 : 0;
            }
            if (buffer.size() > 32768) {
                throw new IOException("websocket headers too large");
            }
        }
        return new String(buffer.toByteArray(), StandardCharsets.ISO_8859_1);
    }

    private static String webSocketAccept(String key) throws Exception {
        MessageDigest sha1 = MessageDigest.getInstance("SHA-1");
        byte[] digest = sha1.digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes(StandardCharsets.ISO_8859_1));
        return Base64.encodeToString(digest, Base64.NO_WRAP);
    }

    private static WebSocketFrame readWebSocketFrame(InputStream input) throws Exception {
        int first = input.read();
        int second = input.read();
        if (first < 0 || second < 0) {
            throw new IOException("websocket closed");
        }
        int opcode = first & 0x0F;
        boolean masked = (second & 0x80) != 0;
        long length = second & 0x7F;
        if (length == 126) {
            length = ((long) readByte(input) << 8) | readByte(input);
        } else if (length == 127) {
            length = 0L;
            for (int i = 0; i < 8; i++) {
                length = (length << 8) | readByte(input);
            }
        }
        if (length > 1024L * 1024L) {
            throw new IOException("websocket frame too large");
        }
        byte[] mask = null;
        if (masked) {
            mask = new byte[]{(byte) readByte(input), (byte) readByte(input), (byte) readByte(input), (byte) readByte(input)};
        }
        byte[] payload = new byte[(int) length];
        int offset = 0;
        while (offset < payload.length) {
            int read = input.read(payload, offset, payload.length - offset);
            if (read < 0) {
                throw new IOException("websocket payload truncated");
            }
            offset += read;
        }
        if (masked) {
            for (int i = 0; i < payload.length; i++) {
                payload[i] = (byte) (payload[i] ^ mask[i % 4]);
            }
        }
        return new WebSocketFrame(opcode, payload);
    }

    private static int readByte(InputStream input) throws Exception {
        int value = input.read();
        if (value < 0) {
            throw new IOException("websocket frame truncated");
        }
        return value & 0xFF;
    }

    private static void writeWebSocketText(OutputStream output, String text) throws Exception {
        writeWebSocketFrame(output, 0x1, text.getBytes(StandardCharsets.UTF_8));
    }

    private static void writeWebSocketFrame(OutputStream output, int opcode, byte[] payload) throws Exception {
        if (payload == null) {
            payload = new byte[0];
        }
        byte[] mask = new byte[4];
        new SecureRandom().nextBytes(mask);
        ByteArrayOutputStream frame = new ByteArrayOutputStream();
        frame.write(0x80 | (opcode & 0x0F));
        int length = payload.length;
        if (length < 126) {
            frame.write(0x80 | length);
        } else if (length <= 65535) {
            frame.write(0x80 | 126);
            frame.write((length >> 8) & 0xFF);
            frame.write(length & 0xFF);
        } else {
            frame.write(0x80 | 127);
            for (int i = 7; i >= 0; i--) {
                frame.write((int) (((long) length >> (8 * i)) & 0xFF));
            }
        }
        frame.write(mask);
        for (int i = 0; i < payload.length; i++) {
            frame.write(payload[i] ^ mask[i % 4]);
        }
        output.write(frame.toByteArray());
        output.flush();
    }

    private static boolean supportsWebSocket(BridgeConfig config) {
        try {
            GatewayEndpoint.parse(config.baseUrl);
            return true;
        } catch (Throwable t) {
            logWebSocketFailure("outbox websocket URL unavailable: " + shortError(t));
            return false;
        }
    }

    private static Socket openGatewaySocket(String host, int port, boolean tls) throws Exception {
        Socket plain = new Socket();
        try {
            plain.connect(new InetSocketAddress(host, port), 5000);
            plain.setSoTimeout(5000);
            if (!tls) {
                return plain;
            }

            SSLSocketFactory factory = (SSLSocketFactory) SSLSocketFactory.getDefault();
            SSLSocket secure = (SSLSocket) factory.createSocket(plain, host, port, true);
            secure.startHandshake();
            if (!HttpsURLConnection.getDefaultHostnameVerifier().verify(host, secure.getSession())) {
                throw new SSLHandshakeException("TLS hostname verification failed for " + host);
            }
            return secure;
        } catch (Throwable t) {
            try {
                plain.close();
            } catch (IOException ignored) {
            }
            throw t;
        }
    }

    private static String urlEncode(String value) {
        if (value == null) {
            return "";
        }
        StringBuilder out = new StringBuilder(value.length() + 16);
        for (int i = 0; i < value.length(); i++) {
            char ch = value.charAt(i);
            if ((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~') {
                out.append(ch);
            } else {
                byte[] bytes = String.valueOf(ch).getBytes(StandardCharsets.UTF_8);
                for (byte b : bytes) {
                    out.append('%');
                    int v = b & 0xFF;
                    char high = Character.toUpperCase(Character.forDigit((v >> 4) & 0xF, 16));
                    char low = Character.toUpperCase(Character.forDigit(v & 0xF, 16));
                    out.append(high).append(low);
                }
            }
        }
        return out.toString();
    }

    private static void logWebSocketFailure(String message) {
        long now = System.currentTimeMillis();
        if (now - LAST_WEBSOCKET_FAIL_LOG_AT < 30000L) {
            return;
        }
        LAST_WEBSOCKET_FAIL_LOG_AT = now;
        log(message);
    }

    private static SendResult sendText(ClassLoader classLoader, String wxid, String text) {
        final ClassLoader targetClassLoader = classLoader;
        final String targetWxid = wxid;
        final String targetText = text;
        try {
            final PreparedTextSend prepared = callOnMainThread(new Callable<PreparedTextSend>() {
                @Override
                public PreparedTextSend call() {
                    return prepareTextSendOnWeChatThread(targetClassLoader, targetWxid, targetText);
                }
            });
            if (prepared.result != null) {
                return prepared.result;
            }

            final int[] attempts = new int[]{0};
            boolean accepted = QueueSubmissionRetrier.awaitAccepted(
                    new QueueSubmissionRetrier.Attempt() {
                        @Override
                        public boolean submit() throws Exception {
                            attempts[0]++;
                            final Object request = prepared.builderRequest;
                            boolean queued = callOnMainThread(new Callable<Boolean>() {
                                @Override
                                public Boolean call() throws Exception {
                                    return executeSendBuilderRequest(request);
                                }
                            });
                            if (!queued) {
                                log("w11.r1 queue busy; keep same request wxid=" + targetWxid
                                        + " msgType=" + prepared.msgType
                                        + " msgId=" + prepared.chatRecordId
                                        + " attempt=" + attempts[0] + "/" + SEND_QUEUE_MAX_ATTEMPTS);
                            }
                            return queued;
                        }
                    },
                    new QueueSubmissionRetrier.Sleeper() {
                        @Override
                        public void sleep(long delayMillis) throws InterruptedException {
                            Thread.sleep(delayMillis);
                        }
                    },
                    SEND_QUEUE_MAX_ATTEMPTS,
                    SEND_QUEUE_RETRY_DELAY_MS);
            if (!accepted) {
                LocalMessageConfirmation.Result confirmation = confirmLocalMessage(prepared.chatRecordId);
                if (confirmation == LocalMessageConfirmation.Result.SENT) {
                    log("sendText confirmed by local message status wxid=" + targetWxid
                            + " msgType=" + prepared.msgType
                            + " msgId=" + prepared.chatRecordId);
                    return SendResult.sent(prepared.chatRecordId);
                }
                if (confirmation == LocalMessageConfirmation.Result.FAILED) {
                    return SendResult.failed(prepared.chatRecordId,
                            "WeChat local message marked failed; local msg id=" + prepared.chatRecordId);
                }
                return SendResult.failed(prepared.chatRecordId,
                        "WeChat send builder queue stayed busy after "
                                + SEND_QUEUE_MAX_ATTEMPTS + " attempts and local message did not reach a terminal status; local msg id="
                                + prepared.chatRecordId);
            }
            log("sendText queued via w11.r1 builder wxid=" + targetWxid
                    + " msgType=" + prepared.msgType
                    + " msgId=" + prepared.chatRecordId
                    + " attempts=" + attempts[0]);
            return SendResult.sent(prepared.chatRecordId);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return SendResult.failed("WeChat send interrupted while waiting for queue");
        } catch (Throwable t) {
            return SendResult.failed("WeChat send failed on main thread: " + shortError(t));
        }
    }

    private static LocalMessageConfirmation.Result confirmLocalMessage(final long chatRecordId) throws Exception {
        if (chatRecordId <= 0L) {
            return LocalMessageConfirmation.Result.TIMEOUT;
        }
        return LocalMessageConfirmation.awaitTerminal(
                new LocalMessageConfirmation.StatusReader() {
                    @Override
                    public int readStatus() throws Exception {
                        return readLocalMessageStatus(chatRecordId);
                    }
                },
                new LocalMessageConfirmation.Sleeper() {
                    @Override
                    public void sleep(long delayMillis) throws InterruptedException {
                        Thread.sleep(delayMillis);
                    }
                },
                SEND_STATUS_MAX_CHECKS,
                SEND_QUEUE_RETRY_DELAY_MS);
    }

    private static int readLocalMessageStatus(long chatRecordId) throws Exception {
        Object db = LAST_DATABASE;
        if (db == null) {
            return LocalMessageConfirmation.STATUS_UNKNOWN;
        }
        Object cursor = rawQuery(
                db,
                "SELECT status FROM message WHERE msgId = ? LIMIT 1",
                new String[]{String.valueOf(chatRecordId)});
        if (cursor == null) {
            return LocalMessageConfirmation.STATUS_UNKNOWN;
        }
        try {
            Method moveToFirst = findNoArgMethod(cursor.getClass(), "moveToFirst");
            if (!Boolean.TRUE.equals(moveToFirst.invoke(cursor))) {
                return LocalMessageConfirmation.STATUS_UNKNOWN;
            }
            return intColumn(cursor, 0);
        } finally {
            closeQuietly(cursor);
        }
    }

    private static PreparedTextSend prepareTextSendOnWeChatThread(ClassLoader classLoader, String wxid, String text) {
        classLoader = runtimeClassLoader(classLoader);
        if (classLoader == null) {
            return PreparedTextSend.completed(SendResult.failed("WeChat classLoader is not available"));
        }
        if (isBlank(wxid) || isBlank(text)) {
            return PreparedTextSend.completed(SendResult.failed("wxid and text are required"));
        }

        int msgType = resolveMessageType(classLoader, wxid);
        Throwable builderUnavailable = null;
        Throwable directUnavailable = null;
        Throwable eventUnavailable = null;
        try {
            PreparedBuilderSend prepared = prepareSendBuilder(classLoader, wxid, text, msgType);
            return PreparedTextSend.builder(prepared.request, prepared.chatRecordId, msgType);
        } catch (ClassNotFoundException | NoSuchMethodException e) {
            builderUnavailable = e;
            log("w11.r1 builder path unavailable, trying dk5.s5.fj: " + shortError(e));
        } catch (Throwable t) {
            return PreparedTextSend.completed(
                    SendResult.failed("WeChat send failed via w11.r1 builder: " + shortError(t)));
        }

        try {
            long msgId = sendViaNetScene(classLoader, wxid, text, msgType);
            log("sendText sent via w11.r0 NetScene wxid=" + wxid + " msgType=" + msgType + " msgId=" + msgId);
            return PreparedTextSend.completed(SendResult.sent(msgId));
        } catch (ClassNotFoundException | NoSuchMethodException e) {
            directUnavailable = e;
            log("w11.r0 NetScene path unavailable, trying dk5.s5.fj: " + shortError(e));
        } catch (Throwable t) {
            directUnavailable = t;
            log("w11.r0 NetScene send failed, trying dk5.s5.fj: " + shortError(t));
        }

        try {
            boolean published = sendViaSendMsgEvent(classLoader, wxid, text, msgType);
            if (!published) {
                throw new IllegalStateException("SendMsgEvent had no listener");
            }
            log("sendText published SendMsgEvent wxid=" + wxid + " msgType=" + msgType);
            return PreparedTextSend.completed(SendResult.sent(0L));
        } catch (ClassNotFoundException | NoSuchMethodException e) {
            eventUnavailable = e;
            log("SendMsgEvent path unavailable, trying dk5.s5.fj: " + shortError(e));
        } catch (Throwable t) {
            eventUnavailable = t;
            log("SendMsgEvent send failed, trying dk5.s5.fj: " + shortError(t));
        }

        try {
            sendViaSendMsgMgr(classLoader, wxid, text, msgType);
            log("sendText sent via dk5.s5.fj wxid=" + wxid + " msgType=" + msgType);
            return PreparedTextSend.completed(SendResult.sent(0L));
        } catch (Throwable t) {
            return PreparedTextSend.completed(
                    SendResult.failed("WeChat send failed via dk5.s5.fj fallback: "
                            + shortError(t)
                            + "; event unavailable: " + shortError(eventUnavailable)
                            + "; direct unavailable: " + shortError(directUnavailable)
                            + "; builder unavailable: " + shortError(builderUnavailable)));
        }
    }

    private static boolean sendViaSendMsgEvent(ClassLoader classLoader, String wxid, String text, int msgType) throws Exception {
        Object event = findClass(classLoader, "com.tencent.mm.autogen.events.SendMsgEvent")
                .getDeclaredConstructor()
                .newInstance();
        Field payloadField = findFieldAny(event.getClass(), "f71992g", "g");
        Object payload = payloadField.get(event);
        setObjectField(payload, wxid, "f7337a", "a");
        setObjectField(payload, text, "f7338b", "b");
        setIntFieldAny(payload, msgType, "f7339c", "c");
        setIntFieldAny(payload, 0, "f7340d", "d");
        Object result = findNoArgMethod(event.getClass(), "e").invoke(event);
        return result instanceof Boolean && (Boolean) result;
    }

    private static <T> T callOnMainThread(Callable<T> callable) throws Exception {
        if (Looper.myLooper() == Looper.getMainLooper()) {
            return callable.call();
        }
        FutureTask<T> task = new FutureTask<>(callable);
        new Handler(Looper.getMainLooper()).post(task);
        return task.get();
    }

    private static long sendViaNetScene(ClassLoader classLoader, String wxid, String text, int msgType) throws Exception {
        Class<?> netSceneClass = findClass(classLoader, "w11.r0");
        Constructor<?> ctor = findConstructor(netSceneClass, String.class, String.class, int.class, int.class, long.class);
        Object scene = ctor.newInstance(wxid, text, msgType, 4, 0L);
        long msgId = getLongField(scene, "f459357f", "f");
        if (msgId == -1L) {
            throw new IllegalStateException("w11.r0 inserted local msg failed");
        }

        Class<?> runCgi = findClass(classLoader, "com.tencent.mm.modelbase.z2");
        Object ready = findNoArgMethod(runCgi, "c").invoke(null);
        if (ready instanceof Boolean && !((Boolean) ready)) {
            throw new IllegalStateException("NetSceneQueue is not ready");
        }

        Method enqueue = findMethod(runCgi, "b", findClass(classLoader, "com.tencent.mm.modelbase.m1"));
        enqueue.invoke(null, scene);
        return msgId;
    }

    private static void sendViaSendMsgMgr(ClassLoader classLoader, String wxid, String text, int msgType) throws Exception {
        Class<?> accessorClass = findClass(classLoader, "tg3.t1");
        Method accessor = findNoArgMethod(accessorClass, "a");
        Object service = accessor.invoke(null);
        if (service == null) {
            throw new IllegalStateException("tg3.t1.a returned null");
        }
        Method send = findMethod(service.getClass(), "fj", String.class, String.class, int.class, int.class);
        send.invoke(service, wxid, text, msgType, 0);
    }

    private static PreparedBuilderSend prepareSendBuilder(ClassLoader classLoader, String wxid, String text, int msgType) throws Exception {
        ensureSendBuilderFactory(classLoader);
        Class<?> builderFactory = findClass(classLoader, "w11.s1");
        Method create = findMethod(builderFactory, "a", String.class);
        Object builder = create.invoke(null, wxid);
        if (builder == null) {
            throw new IllegalStateException("w11.s1.a returned null");
        }

        findMethod(builder.getClass(), "g", String.class).invoke(builder, wxid);
        findMethod(builder.getClass(), "e", String.class).invoke(builder, text);
        findMethod(builder.getClass(), "h", int.class).invoke(builder, msgType);
        setForwardInfo(classLoader, builder);
        setOptionalIntField(builder, 0, "f459371f", "f");
        setOptionalIntField(builder, 4, "f459374i", "i");

        Object request = findNoArgMethod(builder.getClass(), "a").invoke(builder);
        if (request == null) {
            throw new IllegalStateException("w11.r1.a returned null");
        }
        long chatRecordId = getOptionalLongField(request, "f459336b", "b");
        return new PreparedBuilderSend(request, chatRecordId);
    }

    private static boolean executeSendBuilderRequest(Object request) throws Exception {
        Object result = findNoArgMethod(request.getClass(), "a").invoke(request);
        return !(result instanceof Boolean) || (Boolean) result;
    }

    private static void ensureSendBuilderFactory(ClassLoader classLoader) throws Exception {
        Class<?> builderFactory = findClass(classLoader, "w11.s1");
        Field factoryField = findFieldAny(builderFactory, "f459386a", "a");
        if (factoryField.get(null) != null) {
            return;
        }
        Object factory = newInstanceAny(findClass(classLoader, "aq1.l"));
        factoryField.set(null, factory);
        log("initialized w11.s1 send factory with aq1.l");
    }

    private static void setForwardInfo(ClassLoader classLoader, Object builder) {
        try {
            Object forwardInfo = findClass(classLoader, "c01.h7").getDeclaredConstructor().newInstance();
            findMethod(builder.getClass(), "f", forwardInfo.getClass()).invoke(builder, forwardInfo);
        } catch (Throwable t) {
            log("forward info setup skipped: " + shortError(t));
        }
    }

    private static int resolveMessageType(ClassLoader classLoader, String wxid) {
        try {
            Class<?> typeResolver = findClass(classLoader, "c01.e2");
            Object result = findMethod(typeResolver, "C", String.class).invoke(null, wxid);
            if (result instanceof Number) {
                int value = ((Number) result).intValue();
                return value > 0 ? value : 1;
            }
            if (result != null) {
                int value = Integer.parseInt(String.valueOf(result));
                return value > 0 ? value : 1;
            }
        } catch (Throwable t) {
            log("resolve message type failed, fallback to text type 1: " + shortError(t));
        }
        return 1;
    }

    private static Class<?> findClass(ClassLoader classLoader, String name) throws ClassNotFoundException {
        return Class.forName(name, false, classLoader);
    }

    private static Object newInstanceAny(Class<?> cls) throws Exception {
        try {
            Constructor<?> ctor = cls.getDeclaredConstructor();
            ctor.setAccessible(true);
            return ctor.newInstance();
        } catch (NoSuchMethodException ignored) {
            Constructor<?>[] constructors = cls.getDeclaredConstructors();
            for (Constructor<?> ctor : constructors) {
                ctor.setAccessible(true);
                try {
                    Class<?>[] types = ctor.getParameterTypes();
                    Object[] args = new Object[types.length];
                    for (int i = 0; i < types.length; i++) {
                        args[i] = defaultValue(types[i]);
                    }
                    return ctor.newInstance(args);
                } catch (Throwable t) {
                    log("constructor " + cls.getName() + "(" + ctor.getParameterTypes().length + ") failed: " + shortError(t));
                }
            }
            throw new NoSuchMethodException(cls.getName() + " constructors=" + describeConstructors(constructors));
        }
    }

    private static Object defaultValue(Class<?> type) {
        if (!type.isPrimitive()) {
            return null;
        }
        if (type == boolean.class) {
            return false;
        }
        if (type == byte.class) {
            return (byte) 0;
        }
        if (type == short.class) {
            return (short) 0;
        }
        if (type == int.class) {
            return 0;
        }
        if (type == long.class) {
            return 0L;
        }
        if (type == float.class) {
            return 0F;
        }
        if (type == double.class) {
            return 0D;
        }
        if (type == char.class) {
            return (char) 0;
        }
        return null;
    }

    private static Method findNoArgMethod(Class<?> cls, String name) throws NoSuchMethodException {
        return findMethod(cls, name);
    }

    private static Method findMethod(Class<?> cls, String name, Class<?>... parameterTypes) throws NoSuchMethodException {
        Class<?> current = cls;
        while (current != null) {
            try {
                Method method = current.getDeclaredMethod(name, parameterTypes);
                method.setAccessible(true);
                return method;
            } catch (NoSuchMethodException ignored) {
                current = current.getSuperclass();
            }
        }
        Method method = cls.getMethod(name, parameterTypes);
        method.setAccessible(true);
        return method;
    }

    private static Constructor<?> findConstructor(Class<?> cls, Class<?>... parameterTypes) throws NoSuchMethodException {
        Constructor<?> ctor = cls.getDeclaredConstructor(parameterTypes);
        ctor.setAccessible(true);
        return ctor;
    }

    private static void setIntField(Object target, String name, int value) throws Exception {
        Field field = findField(target.getClass(), name);
        field.setInt(target, value);
    }

    private static void setOptionalIntField(Object target, int value, String... names) {
        try {
            Field field = findFieldAny(target.getClass(), names);
            field.setInt(target, value);
        } catch (NoSuchFieldException e) {
            log("optional fields " + joinNames(names) + " not found on " + target.getClass().getName());
        } catch (Throwable t) {
            log("optional fields " + joinNames(names) + " set failed: " + shortError(t));
        }
    }

    private static void setIntFieldAny(Object target, int value, String... names) throws Exception {
        Field field = findFieldAny(target.getClass(), names);
        field.setInt(target, value);
    }

    private static void setObjectField(Object target, Object value, String... names) throws Exception {
        Field field = findFieldAny(target.getClass(), names);
        field.set(target, value);
    }

    private static Field findFieldAny(Class<?> cls, String... names) throws NoSuchFieldException {
        NoSuchFieldException last = null;
        for (String name : names) {
            try {
                return findField(cls, name);
            } catch (NoSuchFieldException e) {
                last = e;
            }
        }
        throw last == null ? new NoSuchFieldException("") : last;
    }

    private static long getLongField(Object target, String... names) throws Exception {
        Field field = findFieldAny(target.getClass(), names);
        Object value = field.get(target);
        if (value instanceof Number) {
            return ((Number) value).longValue();
        }
        return Long.parseLong(String.valueOf(value));
    }

    private static long getOptionalLongField(Object target, String... names) {
        try {
            return getLongField(target, names);
        } catch (Throwable t) {
            log("optional long fields " + joinNames(names) + " read failed: " + shortError(t));
            return 0L;
        }
    }

    private static final class PreparedBuilderSend {
        private final Object request;
        private final long chatRecordId;

        private PreparedBuilderSend(Object request, long chatRecordId) {
            this.request = request;
            this.chatRecordId = chatRecordId;
        }
    }

    private static final class PreparedTextSend {
        private final SendResult result;
        private final Object builderRequest;
        private final long chatRecordId;
        private final int msgType;

        private PreparedTextSend(SendResult result, Object builderRequest, long chatRecordId, int msgType) {
            this.result = result;
            this.builderRequest = builderRequest;
            this.chatRecordId = chatRecordId;
            this.msgType = msgType;
        }

        private static PreparedTextSend completed(SendResult result) {
            return new PreparedTextSend(result, null, 0L, 0);
        }

        private static PreparedTextSend builder(Object request, long chatRecordId, int msgType) {
            return new PreparedTextSend(null, request, chatRecordId, msgType);
        }
    }

    private static String joinNames(String... names) {
        StringBuilder out = new StringBuilder();
        for (String name : names) {
            if (out.length() > 0) {
                out.append("/");
            }
            out.append(name);
        }
        return out.toString();
    }

    private static String describeConstructors(Constructor<?>[] constructors) {
        if (constructors == null || constructors.length == 0) {
            return "[]";
        }
        StringBuilder out = new StringBuilder("[");
        for (int i = 0; i < constructors.length; i++) {
            if (i > 0) {
                out.append(", ");
            }
            Class<?>[] types = constructors[i].getParameterTypes();
            out.append("(");
            for (int j = 0; j < types.length; j++) {
                if (j > 0) {
                    out.append(",");
                }
                out.append(types[j].getName());
            }
            out.append(")");
        }
        out.append("]");
        return out.toString();
    }

    private static Field findField(Class<?> cls, String name) throws NoSuchFieldException {
        Class<?> current = cls;
        while (current != null) {
            try {
                Field field = current.getDeclaredField(name);
                field.setAccessible(true);
                return field;
            } catch (NoSuchFieldException ignored) {
                current = current.getSuperclass();
            }
        }
        throw new NoSuchFieldException(name);
    }

    private static String postJson(BridgeConfig config, String path, String bodyJson) throws Exception {
        GatewayEndpoint endpoint = GatewayEndpoint.parse(config.baseUrl);
        if (!endpoint.isTls()) {
            return postJsonSocket(config, path, bodyJson);
        }
        try {
            return postJsonOnce(config, path, bodyJson);
        } catch (IOException first) {
            log("postJson retry after io error: " + shortError(first));
            System.setProperty("http.keepAlive", "false");
            return postJsonOnce(config, path, bodyJson);
        }
    }

    private static String postJsonSocket(BridgeConfig config, String path, String bodyJson) throws Exception {
        GatewayEndpoint endpoint = GatewayEndpoint.parse(config.baseUrl);
        URL url = endpoint.resolve(path);
        String requestPath = url.getFile();
        if (isBlank(requestPath)) {
            requestPath = "/";
        }
        int port = endpoint.port();
        String host = endpoint.host();
        String hostHeader = endpoint.hostHeader();
        byte[] body = bodyJson.getBytes(StandardCharsets.UTF_8);
        log("postJsonSocket path=" + path + " host=" + hostHeader + " bodyBytes=" + body.length);

        StringBuilder headers = new StringBuilder();
        headers.append("POST ").append(requestPath).append(" HTTP/1.1\r\n");
        headers.append("Host: ").append(hostHeader).append("\r\n");
        headers.append("User-Agent: WechatObservatoryModule/0.1\r\n");
        headers.append("Accept: application/json\r\n");
        headers.append("Content-Type: application/json; charset=utf-8\r\n");
        headers.append("Content-Length: ").append(body.length).append("\r\n");
        headers.append("Connection: close\r\n\r\n");

        ByteArrayOutputStream response = new ByteArrayOutputStream();
        try (Socket socket = new Socket()) {
            socket.connect(new InetSocketAddress(host, port), 5000);
            socket.setSoTimeout(5000);
            OutputStream out = socket.getOutputStream();
            out.write(headers.toString().getBytes(StandardCharsets.ISO_8859_1));
            out.write(body);
            out.flush();
            socket.shutdownOutput();

            InputStream input = socket.getInputStream();
            byte[] buffer = new byte[4096];
            int read;
            while ((read = input.read(buffer)) != -1) {
                response.write(buffer, 0, read);
            }
        }

        byte[] responseBytes = response.toByteArray();
        if (responseBytes.length == 0) {
            throw new IOException("bridge returned empty socket response");
        }
        String responseText = new String(responseBytes, StandardCharsets.ISO_8859_1);
        int headerEnd = responseText.indexOf("\r\n\r\n");
        if (headerEnd < 0) {
            throw new IOException("bridge returned malformed socket response");
        }
        int statusEnd = responseText.indexOf("\r\n");
        String statusLine = statusEnd < 0 ? responseText : responseText.substring(0, statusEnd);
        String[] statusParts = statusLine.split(" ");
        int status = statusParts.length >= 2 ? Integer.parseInt(statusParts[1]) : 0;
        byte[] bodyBytes = new byte[responseBytes.length - headerEnd - 4];
        System.arraycopy(responseBytes, headerEnd + 4, bodyBytes, 0, bodyBytes.length);

        String headersText = responseText.substring(0, headerEnd).toLowerCase(Locale.US);
        String responseBody = headersText.contains("transfer-encoding: chunked")
                ? decodeChunkedBody(bodyBytes)
                : new String(bodyBytes, StandardCharsets.UTF_8);
        if (status < 200 || status >= 300) {
            throw new IllegalStateException("bridge returned HTTP " + status + ": " + responseBody);
        }
        return responseBody;
    }

    private static String decodeChunkedBody(byte[] bodyBytes) throws Exception {
        ByteArrayOutputStream decoded = new ByteArrayOutputStream();
        int offset = 0;
        while (offset < bodyBytes.length) {
            int lineEnd = indexOfCrlf(bodyBytes, offset);
            if (lineEnd < 0) {
                break;
            }
            String sizeLine = new String(bodyBytes, offset, lineEnd - offset, StandardCharsets.ISO_8859_1).trim();
            int semicolon = sizeLine.indexOf(';');
            if (semicolon >= 0) {
                sizeLine = sizeLine.substring(0, semicolon);
            }
            int chunkSize = Integer.parseInt(sizeLine, 16);
            offset = lineEnd + 2;
            if (chunkSize == 0) {
                break;
            }
            if (offset + chunkSize > bodyBytes.length) {
                throw new IOException("truncated chunked response");
            }
            decoded.write(bodyBytes, offset, chunkSize);
            offset += chunkSize + 2;
        }
        return new String(decoded.toByteArray(), StandardCharsets.UTF_8);
    }

    private static int indexOfCrlf(byte[] bytes, int start) {
        for (int i = start; i + 1 < bytes.length; i++) {
            if (bytes[i] == '\r' && bytes[i + 1] == '\n') {
                return i;
            }
        }
        return -1;
    }

    private static String postJsonOnce(BridgeConfig config, String path, String bodyJson) throws Exception {
        URL url = GatewayEndpoint.parse(config.baseUrl).resolve(path);
        HttpURLConnection connection = (HttpURLConnection) url.openConnection();
        connection.setConnectTimeout(5000);
        connection.setReadTimeout(5000);
        connection.setRequestMethod("POST");
        connection.setDoOutput(true);
        connection.setRequestProperty("Content-Type", "application/json; charset=utf-8");
        connection.setRequestProperty("Connection", "close");

        byte[] body = bodyJson.getBytes(StandardCharsets.UTF_8);
        connection.setFixedLengthStreamingMode(body.length);
        try (OutputStream out = connection.getOutputStream()) {
            out.write(body);
        }

        int status = connection.getResponseCode();
        String response = readResponse(status >= 200 && status < 300 ? connection.getInputStream() : connection.getErrorStream());
        connection.disconnect();
        if (status < 200 || status >= 300) {
            throw new IllegalStateException("bridge returned HTTP " + status + ": " + response);
        }
        return response;
    }

    private static String readResponse(InputStream input) throws Exception {
        if (input == null) {
            return "";
        }
        StringBuilder out = new StringBuilder();
        try (BufferedReader reader = new BufferedReader(new InputStreamReader(input, StandardCharsets.UTF_8))) {
            String line;
            while ((line = reader.readLine()) != null) {
                out.append(line);
            }
        }
        return out.toString();
    }

    private static String stringArg(Object[] args, int index) {
        if (args == null || args.length <= index || args[index] == null) {
            return "";
        }
        return String.valueOf(args[index]);
    }

    private static long normalizeCreateTime(Long createTime) {
        return normalizeCreateTime(createTime == null ? 0L : createTime.longValue());
    }

    private static long normalizeCreateTime(long createTime) {
        if (createTime <= 0) {
            return System.currentTimeMillis() / 1000L;
        }
        return createTime > 10_000_000_000L ? createTime / 1000L : createTime;
    }

    private static void log(String message) {
        BridgeLogger.log(message);
    }

}

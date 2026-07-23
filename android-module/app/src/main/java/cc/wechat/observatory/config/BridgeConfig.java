package cc.wechat.observatory.config;

import android.content.Context;
import android.database.Cursor;
import android.net.Uri;
import android.os.Environment;
import android.content.SharedPreferences;

import java.io.File;
import java.io.FileInputStream;
import java.lang.reflect.Method;
import java.util.Locale;
import java.util.Properties;
import java.util.Map;

import cc.wechat.observatory.util.BridgeLogger;
import cc.wechat.observatory.util.Strings;
import de.robv.android.xposed.XSharedPreferences;

public final class BridgeConfig {
    private static final String CONFIG_PROVIDER_URI = "content://cc.wechat.observatory.config/config";
    private static final String MODULE_PACKAGE = "cc.wechat.observatory";
    private static final String PREFS_NAME = "bridge_config";
    private static final String LOCAL_TMP_CONFIG = "/data/local/tmp/wechat-observatory/config.properties";
    private static volatile long lastConfigLogAt = 0L;
    private static volatile long lastProviderConfigLogAt = 0L;

    public boolean enabled;
    public String baseUrl;
    public String device;
    public String selfWxid;
    public String apiKey;
    public String nickname;
    public long pollIntervalMs;
    public int pollLimit;
    public long contactSyncIntervalMs;
    public int contactSyncLimit;
    public boolean includeChatrooms;
    public boolean mediaUploadEnabled;
    public long mediaUploadLimitBytes;
    public String signature;

    private BridgeConfig() {
    }

    public static BridgeConfig load(Context context) {
        Properties properties = readProperties(context);
        BridgeConfig config = new BridgeConfig();
        config.enabled = !"0".equals(setting(properties, "enabled", "1"));
        config.baseUrl = setting(properties, "bridge_url", "");
        config.device = "";
        config.selfWxid = "";
        config.apiKey = setting(properties, "api_key", "");
        config.nickname = "";
        config.pollIntervalMs = longSetting(properties, "poll_interval_ms", 1000L);
        config.pollLimit = (int) longSetting(properties, "poll_limit", 20L);
        config.contactSyncIntervalMs = longSetting(properties, "contact_sync_interval_ms", 600000L);
        config.contactSyncLimit = (int) longSetting(properties, "contact_sync_limit", 1000L);
        config.includeChatrooms = booleanSetting(properties, "contact_include_chatrooms", true);
        config.mediaUploadEnabled = booleanSetting(properties, "media_upload_enabled", false);
        config.mediaUploadLimitBytes = longSetting(properties, "media_upload_limit_bytes", 5L * 1024L * 1024L);
        config.signature = configSignature(properties);
        logConfigOnce(config, properties);
        return config;
    }

    private static Properties readProperties(Context context) {
        Properties localTmpProperties = readLocalTmpProperties();
        if (!localTmpProperties.isEmpty()) {
            return localTmpProperties;
        }

        Properties xposedProperties = readXposedPreferences();
        if (!xposedProperties.isEmpty()) {
            return xposedProperties;
        }

        Properties providerProperties = readProviderProperties(context);
        if (!providerProperties.isEmpty()) {
            return providerProperties;
        }

        Properties packagePreferences = readPackagePreferences(context);
        if (!packagePreferences.isEmpty()) {
            return packagePreferences;
        }

        Properties properties = new Properties();
        File root = Environment.getExternalStorageDirectory();
        File[] candidates = new File[]{
                new File(LOCAL_TMP_CONFIG),
                new File(root, "Android/media/cc.wechat.observatory/config.properties"),
                new File(root, "Download/wechat-observatory/config.properties")
        };
        for (File file : candidates) {
            if (!file.isFile()) {
                continue;
            }
            try {
                try (FileInputStream input = new FileInputStream(file)) {
                    properties.load(input);
                }
                return properties;
            } catch (Throwable t) {
                BridgeLogger.log("read config failed from " + file.getAbsolutePath() + ": " + t);
            }
        }
        return properties;
    }

    private static Properties readPackagePreferences(Context context) {
        Properties properties = new Properties();
        if (context == null) {
            return properties;
        }
        try {
            Context moduleContext = context.createPackageContext(
                    MODULE_PACKAGE,
                    Context.CONTEXT_IGNORE_SECURITY);
            SharedPreferences prefs = moduleContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE);
            Map<String, ?> all = prefs.getAll();
            if (all == null || all.isEmpty()) {
                return properties;
            }
            for (Map.Entry<String, ?> entry : all.entrySet()) {
                if (!Strings.isBlank(entry.getKey()) && entry.getValue() != null) {
                    properties.setProperty(entry.getKey(), String.valueOf(entry.getValue()));
                }
            }
            BridgeLogger.log("read config from package preferences keys=" + properties.size());
        } catch (Throwable t) {
            logProviderConfigOnce("read config from package preferences failed: " + Strings.shortError(t));
        }
        return properties;
    }

    private static Properties readLocalTmpProperties() {
        Properties properties = new Properties();
        File mirror = new File(LOCAL_TMP_CONFIG);
        if (!mirror.isFile()) {
            return properties;
        }
        try {
            try (FileInputStream input = new FileInputStream(mirror)) {
                properties.load(input);
            }
            if (!properties.isEmpty()) {
                BridgeLogger.log("read config from local tmp keys=" + properties.size());
            }
        } catch (Throwable t) {
            logProviderConfigOnce("read config from local tmp failed: " + Strings.shortError(t));
        }
        return properties;
    }

    private static Properties readXposedPreferences() {
        Properties properties = new Properties();
        try {
            XSharedPreferences prefs = new XSharedPreferences(MODULE_PACKAGE, PREFS_NAME);
            prefs.reload();
            Map<String, ?> all = prefs.getAll();
            if (all == null || all.isEmpty()) {
                return properties;
            }
            for (Map.Entry<String, ?> entry : all.entrySet()) {
                if (!Strings.isBlank(entry.getKey()) && entry.getValue() != null) {
                    properties.setProperty(entry.getKey(), String.valueOf(entry.getValue()));
                }
            }
            BridgeLogger.log("read config from xshared preferences keys=" + properties.size());
        } catch (Throwable t) {
            logProviderConfigOnce("read config from xshared preferences failed: " + Strings.shortError(t));
        }
        return properties;
    }

    private static Properties readProviderProperties(Context context) {
        Properties properties = new Properties();
        if (context == null) {
            logProviderConfigOnce("read config from provider skipped: no context");
            return properties;
        }
        Context moduleContext = moduleContext(context);
        if (moduleContext != null) {
            readProviderPropertiesFromContext(moduleContext, properties, "module");
        }
        if (!properties.isEmpty()) {
            return properties;
        }
        readProviderPropertiesFromContext(context, properties, "process");
        if (!properties.isEmpty()) {
            return properties;
        }
        Context systemContext = systemContext();
        if (systemContext != null && systemContext != context) {
            readProviderPropertiesFromContext(systemContext, properties, "system");
        }
        return properties;
    }

    private static Context moduleContext(Context context) {
        if (context == null) {
            return null;
        }
        try {
            return context.createPackageContext(MODULE_PACKAGE, Context.CONTEXT_IGNORE_SECURITY);
        } catch (Throwable t) {
            logProviderConfigOnce("create module context failed: " + Strings.shortError(t));
            return null;
        }
    }

    private static void readProviderPropertiesFromContext(Context context, Properties properties, String source) {
        if (context == null || properties == null) {
            return;
        }
        Cursor cursor = null;
        try {
            cursor = context.getContentResolver().query(
                    Uri.parse(CONFIG_PROVIDER_URI),
                    new String[]{"key", "value"},
                    null,
                    null,
                    null);
            if (cursor == null) {
                logProviderConfigOnce("read config from provider returned null cursor");
                return;
            }
            int keyIndex = cursor.getColumnIndex("key");
            int valueIndex = cursor.getColumnIndex("value");
            if (keyIndex < 0 || valueIndex < 0) {
                return;
            }
            while (cursor.moveToNext()) {
                String key = cursor.getString(keyIndex);
                String value = cursor.getString(valueIndex);
                if (!Strings.isBlank(key) && value != null) {
                    properties.setProperty(key, value);
                }
            }
            if (!properties.isEmpty()) {
                BridgeLogger.log("read config from " + source + " provider keys=" + properties.size());
            }
        } catch (Throwable t) {
            logProviderConfigOnce("read config from " + source + " provider failed: " + Strings.shortError(t));
        } finally {
            if (cursor != null) {
                try {
                    cursor.close();
                } catch (Throwable ignored) {
                }
            }
        }
    }

    private static Context systemContext() {
        try {
            Class<?> activityThreadClass = Class.forName("android.app.ActivityThread");
            Method current = activityThreadClass.getDeclaredMethod("currentActivityThread");
            Object thread = current.invoke(null);
            if (thread == null) {
                return null;
            }
            Method getSystemContext = activityThreadClass.getDeclaredMethod("getSystemContext");
            Object value = getSystemContext.invoke(thread);
            if (value instanceof Context) {
                return (Context) value;
            }
        } catch (Throwable t) {
            logProviderConfigOnce("get system context failed: " + Strings.shortError(t));
        }
        return null;
    }

    private static void logConfigOnce(BridgeConfig config, Properties properties) {
        long now = System.currentTimeMillis();
        if (now - lastConfigLogAt < 30000L) {
            return;
        }
        lastConfigLogAt = now;
        BridgeLogger.log("config loaded keys=" + properties.size()
                + " enabled=" + config.enabled
                + " baseUrl=" + (Strings.isBlank(config.baseUrl) ? "<empty>" : config.baseUrl)
                + " device=" + config.device
                + " selfWxid=" + (Strings.isBlank(config.selfWxid) ? "<empty>" : config.selfWxid)
                + " apiKey=" + (Strings.isBlank(config.apiKey) ? "<empty>" : "<set>")
                + " pollIntervalMs=" + config.pollIntervalMs
                + " includeChatrooms=" + config.includeChatrooms);
    }

    private static void logProviderConfigOnce(String message) {
        long now = System.currentTimeMillis();
        if (now - lastProviderConfigLogAt < 30000L) {
            return;
        }
        lastProviderConfigLogAt = now;
        BridgeLogger.log(message);
    }

    private static String setting(Properties properties, String name, String fallback) {
        try {
            String value = properties.getProperty(name);
            return Strings.isBlank(value) ? fallback : value.trim();
        } catch (Throwable t) {
            return fallback;
        }
    }

    private static long longSetting(Properties properties, String name, long fallback) {
        try {
            String value = properties.getProperty(name);
            return Strings.isBlank(value) ? fallback : Long.parseLong(value.trim());
        } catch (Throwable t) {
            return fallback;
        }
    }

    private static boolean booleanSetting(Properties properties, String name, boolean fallback) {
        try {
            String value = properties.getProperty(name);
            if (Strings.isBlank(value)) {
                return fallback;
            }
            value = value.trim().toLowerCase(Locale.US);
            return "1".equals(value) || "true".equals(value) || "yes".equals(value) || "on".equals(value);
        } catch (Throwable t) {
            return fallback;
        }
    }

    private static String configSignature(Properties properties) {
        StringBuilder out = new StringBuilder();
        for (String key : new String[]{
                "enabled",
                "bridge_url",
                "api_key",
                "poll_interval_ms",
                "poll_limit",
                "contact_sync_interval_ms",
                "contact_sync_limit",
                "contact_include_chatrooms",
                "media_upload_enabled",
                "media_upload_limit_bytes"
        }) {
            out.append(key).append('=').append(setting(properties, key, "")).append('\n');
        }
        return out.toString();
    }
}

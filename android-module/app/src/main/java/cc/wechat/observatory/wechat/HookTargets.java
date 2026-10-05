package cc.wechat.observatory.wechat;

import java.util.Properties;
import java.util.regex.Pattern;

import cc.wechat.observatory.util.Strings;

/**
 * Which WeChat internals this module hooks.
 *
 * <p>WeChat re-obfuscates internal class and method names on every release. The
 * built-in defaults below are the names verified against the profiles shipped
 * under {@code assets/profiles}. A deployment may override them from the bridge
 * config so a renamed class does not require shipping a new APK, but an override
 * is accepted only when it declares the WeChat build it was written for and every
 * name is syntactically valid.
 *
 * <p>A rejected override never partially applies: the module keeps the built-in
 * default for that field and records why, because a wrong hook target silently
 * replacing a working one is worse than no override at all.
 */
public final class HookTargets {
    public static final String DEFAULT_OBSERVATION_CLASS = "com.tencent.wcdb.database.SQLiteDatabase";
    public static final String DEFAULT_OBSERVATION_METHOD = "insertWithOnConflict";
    public static final String DEFAULT_APP_CLASS = "com.tencent.mm.app.MMApplicationLike";
    public static final String DEFAULT_APP_ATTACH_METHOD = "onBaseContextAttached";
    public static final String DEFAULT_APP_CREATE_METHOD = "onCreate";

    /** Bind key holding the WeChat build the override was written for. */
    public static final String KEY_BIND = "hook_override_bind";
    public static final String KEY_OBSERVATION_CLASS = "hook_observation_class";
    public static final String KEY_OBSERVATION_METHOD = "hook_observation_method";
    public static final String KEY_APP_CLASS = "hook_app_class";
    public static final String KEY_APP_ATTACH_METHOD = "hook_app_attach_method";
    public static final String KEY_APP_CREATE_METHOD = "hook_app_create_method";

    public static final String[] OVERRIDE_KEYS = new String[]{
            KEY_OBSERVATION_CLASS,
            KEY_OBSERVATION_METHOD,
            KEY_APP_CLASS,
            KEY_APP_ATTACH_METHOD,
            KEY_APP_CREATE_METHOD
    };

    private static final Pattern CLASS_NAME =
            Pattern.compile("[A-Za-z_$][A-Za-z0-9_$]*(\\.[A-Za-z_$][A-Za-z0-9_$]*)+");
    private static final Pattern METHOD_NAME =
            Pattern.compile("[A-Za-z_$][A-Za-z0-9_$]*");

    public final String observationClass;
    public final String observationMethod;
    public final String appClass;
    public final String appAttachMethod;
    public final String appCreateMethod;
    /** True when at least one field came from a validated override. */
    public final boolean overridden;
    /** First reason an override was refused, or an empty string. */
    public final String rejection;

    private HookTargets(
            String observationClass,
            String observationMethod,
            String appClass,
            String appAttachMethod,
            String appCreateMethod,
            boolean overridden,
            String rejection) {
        this.observationClass = observationClass;
        this.observationMethod = observationMethod;
        this.appClass = appClass;
        this.appAttachMethod = appAttachMethod;
        this.appCreateMethod = appCreateMethod;
        this.overridden = overridden;
        this.rejection = rejection == null ? "" : rejection;
    }

    public static HookTargets defaults() {
        return new HookTargets(
                DEFAULT_OBSERVATION_CLASS,
                DEFAULT_OBSERVATION_METHOD,
                DEFAULT_APP_CLASS,
                DEFAULT_APP_ATTACH_METHOD,
                DEFAULT_APP_CREATE_METHOD,
                false,
                "");
    }

    /**
     * @param properties only the {@code hook_*} config keys, or null.
     * @param runtimeBind the running WeChat build, formatted as
     *     {@code <versionName>/<versionCode>}. An empty value never satisfies a bind.
     */
    public static HookTargets resolve(Properties properties, String runtimeBind) {
        if (properties == null || properties.isEmpty()) {
            return defaults();
        }
        String bind = setting(properties, KEY_BIND);
        if (Strings.isBlank(bind)) {
            return hasAnyOverride(properties)
                    ? defaults().reject("missing-bind")
                    : defaults();
        }
        if (Strings.isBlank(runtimeBind) || !bind.equals(runtimeBind.trim())) {
            return defaults().reject("bind-mismatch");
        }

        String rejection = "";
        boolean overridden = false;

        String observationClass = setting(properties, KEY_OBSERVATION_CLASS);
        if (!Strings.isBlank(observationClass)) {
            if (CLASS_NAME.matcher(observationClass).matches()) {
                overridden = true;
            } else {
                observationClass = "";
                rejection = firstProblem(rejection, "invalid-class:" + KEY_OBSERVATION_CLASS);
            }
        }

        String observationMethod = setting(properties, KEY_OBSERVATION_METHOD);
        if (!Strings.isBlank(observationMethod)) {
            if (METHOD_NAME.matcher(observationMethod).matches()) {
                overridden = true;
            } else {
                observationMethod = "";
                rejection = firstProblem(rejection, "invalid-method:" + KEY_OBSERVATION_METHOD);
            }
        }

        String appClass = setting(properties, KEY_APP_CLASS);
        if (!Strings.isBlank(appClass)) {
            if (CLASS_NAME.matcher(appClass).matches()) {
                overridden = true;
            } else {
                appClass = "";
                rejection = firstProblem(rejection, "invalid-class:" + KEY_APP_CLASS);
            }
        }

        String appAttachMethod = setting(properties, KEY_APP_ATTACH_METHOD);
        if (!Strings.isBlank(appAttachMethod)) {
            if (METHOD_NAME.matcher(appAttachMethod).matches()) {
                overridden = true;
            } else {
                appAttachMethod = "";
                rejection = firstProblem(rejection, "invalid-method:" + KEY_APP_ATTACH_METHOD);
            }
        }

        String appCreateMethod = setting(properties, KEY_APP_CREATE_METHOD);
        if (!Strings.isBlank(appCreateMethod)) {
            if (METHOD_NAME.matcher(appCreateMethod).matches()) {
                overridden = true;
            } else {
                appCreateMethod = "";
                rejection = firstProblem(rejection, "invalid-method:" + KEY_APP_CREATE_METHOD);
            }
        }

        return new HookTargets(
                Strings.isBlank(observationClass) ? DEFAULT_OBSERVATION_CLASS : observationClass,
                Strings.isBlank(observationMethod) ? DEFAULT_OBSERVATION_METHOD : observationMethod,
                Strings.isBlank(appClass) ? DEFAULT_APP_CLASS : appClass,
                Strings.isBlank(appAttachMethod) ? DEFAULT_APP_ATTACH_METHOD : appAttachMethod,
                Strings.isBlank(appCreateMethod) ? DEFAULT_APP_CREATE_METHOD : appCreateMethod,
                overridden,
                rejection);
    }

    private HookTargets reject(String reason) {
        return new HookTargets(
                observationClass,
                observationMethod,
                appClass,
                appAttachMethod,
                appCreateMethod,
                overridden,
                reason);
    }

    private static String firstProblem(String current, String candidate) {
        return Strings.isBlank(current) ? candidate : current;
    }

    private static boolean hasAnyOverride(Properties properties) {
        for (String key : OVERRIDE_KEYS) {
            if (!Strings.isBlank(setting(properties, key))) {
                return true;
            }
        }
        return false;
    }

    private static String setting(Properties properties, String name) {
        try {
            String value = properties.getProperty(name);
            return value == null ? "" : value.trim();
        } catch (Throwable t) {
            return "";
        }
    }

    /** One-line summary for diagnostics; never empty. */
    public String describe() {
        StringBuilder out = new StringBuilder("source=").append(overridden ? "override" : "builtin");
        if (!Strings.isBlank(rejection)) {
            out.append(" rejected=").append(rejection);
        }
        out.append(" observation=").append(observationClass).append('#').append(observationMethod);
        out.append(" app=").append(appClass).append('#').append(appAttachMethod);
        return out.toString();
    }
}

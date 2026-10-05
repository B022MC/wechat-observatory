package cc.wechat.observatory.wechat;

import java.util.Locale;

/**
 * Which WeChat instance on this phone the module serves.
 *
 * <p>A phone can run WeChat twice: the normal app in the main Android user and
 * a "dual app" / clone in a separate Android user (Xiaomi uses user 999). Both
 * load this module and read the same saved API Key, so without a scope both
 * would try to own the same device binding. The Android user id is derived from
 * the process uid ({@code uid / 100000}, the platform's PER_USER_RANGE).
 */
public final class WeChatScope {
    public static final String KEY = "wechat_scope";

    public static final String MAIN = "main";
    public static final String CLONE = "clone";
    public static final String ALL = "all";

    /** Android's PER_USER_RANGE: every user owns one block of 100000 uids. */
    static final int PER_USER_RANGE = 100000;

    private WeChatScope() {
    }

    /** Normalizes a saved value; anything unknown falls back to the main WeChat only. */
    public static String normalize(String value) {
        if (value == null) {
            return MAIN;
        }
        String normalized = value.trim().toLowerCase(Locale.US);
        if (CLONE.equals(normalized) || ALL.equals(normalized)) {
            return normalized;
        }
        return MAIN;
    }

    public static int androidUserId(int uid) {
        return uid < 0 ? 0 : uid / PER_USER_RANGE;
    }

    /** The main WeChat runs in Android user 0; every other user is a clone. */
    public static boolean isCloneUid(int uid) {
        return androidUserId(uid) != 0;
    }

    /** True when the module should work inside the WeChat running with this uid. */
    public static boolean serves(String scope, int uid) {
        switch (normalize(scope)) {
            case ALL:
                return true;
            case CLONE:
                return isCloneUid(uid);
            default:
                return !isCloneUid(uid);
        }
    }

    /** Short label used in logs and the capability report. */
    public static String describe(int uid) {
        return (isCloneUid(uid) ? CLONE : MAIN) + "/user" + androidUserId(uid);
    }
}

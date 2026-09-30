package cc.wechat.observatory.wechat;

import java.lang.reflect.Method;

/** Read the current kernel's account, including MMKV-backed ConfigStorage on 8.0.78. */
public final class WeChatAccountResolver {
    // Verified against official APK bytecode; never probe arbitrary static factories.
    private static final String[][] BINDINGS = {
            {"gp0.j1", "x", "m"}, // 8.0.78
            {"gm0.j1", "u", "l"}  // 8.0.74
    };

    private WeChatAccountResolver() {
    }

    public static RuntimeAccount resolve(ClassLoader loader) throws Exception {
        for (String[] binding : BINDINGS) {
            Class<?> kernel;
            try {
                kernel = Class.forName(binding[0], false, loader);
            } catch (ClassNotFoundException ignored) {
                continue;
            }
            Object storage = method(kernel, binding[1]).invoke(null);
            return readStorage(storage, binding[2]);
        }
        throw new IllegalStateException("No verified current-account binding for this WeChat version");
    }

    static RuntimeAccount readStorage(Object storage, String getterName) throws Exception {
        if (storage == null) {
            throw new IllegalStateException("WeChat account storage is pending");
        }
        // g() calls the kernel's account-initialization guard before returning its path.
        Method pathGetter = method(storage.getClass(), "g");
        String path = string(pathGetter.invoke(storage));
        Object config = method(storage.getClass(), "c").invoke(storage);
        if (config == null) {
            throw new IllegalStateException("WeChat account configuration is pending");
        }
        Method getter = method(config.getClass(), getterName, int.class, Object.class);
        String wxid = string(getter.invoke(config, 2, ""));
        // Slot 42 is a mutable alias. Wait for the canonical username in slot 2.
        if (!isAccountId(wxid)) {
            throw new IllegalStateException("Current WeChat identifier is pending");
        }
        String nickname = string(getter.invoke(config, 4, ""));
        if (!path.equals(string(pathGetter.invoke(storage)))
                || !wxid.equals(string(getter.invoke(config, 2, "")))) {
            throw new IllegalStateException("WeChat account changed while reading its configuration");
        }
        return new RuntimeAccount(wxid, nickname, path);
    }

    static boolean isAccountId(String value) {
        if (value == null || value.isEmpty() || value.startsWith("acct_")
                || value.length() > 128 || value.matches("[0-9]+")) {
            return false;
        }
        return value.matches("[A-Za-z0-9_.-]+");
    }

    private static String string(Object value) {
        return value instanceof String ? ((String) value).trim() : "";
    }

    private static Method method(Class<?> type, String name, Class<?>... args) throws Exception {
        for (Class<?> current = type; current != null; current = current.getSuperclass()) {
            try {
                Method method = current.getDeclaredMethod(name, args);
                method.setAccessible(true);
                return method;
            } catch (NoSuchMethodException ignored) {
                // ConfigStorage may inherit a version's getter implementation.
            }
        }
        throw new NoSuchMethodException(type.getName() + "." + name);
    }
}

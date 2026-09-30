package cc.wechat.observatory.gateway;

import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ThreadFactory;

import static cc.wechat.observatory.util.Strings.isBlank;
import static cc.wechat.observatory.util.Strings.json;

/**
 * Best-effort upload of registration and runtime failures to the server.
 *
 * <p>Reporting is strictly additive: it runs on its own daemon thread, never
 * throws into the caller, never retries, and never reports its own failures.
 * The same stage and message are sent at most once per repeat window, and at
 * most one report leaves the phone per minimum interval.
 */
public final class DiagnosticsReporter {
    public static final String PATH = "/module/diagnostics";
    static final long REPEAT_WINDOW_MS = 10L * 60L * 1000L;
    static final long MIN_INTERVAL_MS = 15L * 1000L;
    static final int MAX_MESSAGE_CHARS = 900;
    static final int MAX_TRACKED = 64;

    public interface Transport {
        void post(String path, String body) throws Exception;
    }

    public static final class Report {
        public String apiKey;
        public String device;
        public String wxid;
        public String stage;
        public String message;
        public String moduleVersion;
        public String wechatVersion;
        public String android;
    }

    private final ExecutorService executor;
    private final Map<String, Long> lastSent = new HashMap<>();
    private long lastAnySentAt;
    private boolean anySent;

    DiagnosticsReporter(ExecutorService executor) {
        this.executor = executor;
    }

    public static DiagnosticsReporter create() {
        return new DiagnosticsReporter(Executors.newSingleThreadExecutor(new ThreadFactory() {
            @Override
            public Thread newThread(Runnable runnable) {
                Thread thread = new Thread(runnable, "wechat-observatory-diagnostics");
                thread.setDaemon(true);
                return thread;
            }
        }));
    }

    /** Queues one report. Returns whether it was admitted; never throws. */
    public boolean report(final Transport transport, Report report, long nowMs) {
        try {
            if (transport == null || report == null || isBlank(report.stage)) {
                return false;
            }
            String message = report.message == null ? "" : report.message;
            if (!admit(report.stage + "\n" + message, nowMs)) {
                return false;
            }
            final String body = body(report);
            executor.execute(new Runnable() {
                @Override
                public void run() {
                    try {
                        transport.post(PATH, body);
                    } catch (Throwable ignored) {
                        // Diagnostics never report or retry their own failures.
                    }
                }
            });
            return true;
        } catch (Throwable ignored) {
            return false;
        }
    }

    synchronized boolean admit(String key, long nowMs) {
        Long last = lastSent.get(key);
        if (last != null && nowMs - last < REPEAT_WINDOW_MS) {
            return false;
        }
        if (anySent && nowMs - lastAnySentAt < MIN_INTERVAL_MS) {
            return false;
        }
        if (lastSent.size() >= MAX_TRACKED) {
            lastSent.clear();
        }
        lastSent.put(key, nowMs);
        lastAnySentAt = nowMs;
        anySent = true;
        return true;
    }

    static String body(Report report) {
        StringBuilder out = new StringBuilder(256);
        out.append('{');
        field(out, "api_key", report.apiKey, 256, true);
        field(out, "device", report.device, 128, false);
        field(out, "wxid", report.wxid, 191, false);
        field(out, "stage", report.stage, 48, false);
        field(out, "message", report.message, MAX_MESSAGE_CHARS, false);
        field(out, "module_version", report.moduleVersion, 64, false);
        field(out, "wechat_version", report.wechatVersion, 64, false);
        field(out, "android", report.android, 128, false);
        out.append('}');
        return out.toString();
    }

    private static void field(StringBuilder out, String name, String value, int maxChars, boolean first) {
        if (!first) {
            out.append(',');
        }
        String text = value == null ? "" : value.trim();
        if (text.length() > maxChars) {
            text = text.substring(0, maxChars);
        }
        out.append('"').append(name).append("\":\"").append(json(text)).append('"');
    }
}

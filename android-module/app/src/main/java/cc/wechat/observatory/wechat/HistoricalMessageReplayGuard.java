package cc.wechat.observatory.wechat;

/**
 * Limits stale database insert replays while allowing newly created messages.
 */
public final class HistoricalMessageReplayGuard {
    private int admittedStaleMessages;

    public synchronized boolean allows(long nowMillis, long createTimeSeconds, long graceMillis, int staleLimit) {
        if (!isStale(nowMillis, createTimeSeconds, graceMillis)) {
            return true;
        }
        if (staleLimit <= 0 || admittedStaleMessages >= staleLimit) {
            return false;
        }
        admittedStaleMessages++;
        return true;
    }

    static boolean isStale(long nowMillis, long createTimeSeconds, long graceMillis) {
        if (nowMillis <= 0L || createTimeSeconds <= 0L || graceMillis < 0L) {
            return false;
        }
        long createMillis = createTimeSeconds > Long.MAX_VALUE / 1000L
                ? Long.MAX_VALUE
                : createTimeSeconds * 1000L;
        return createMillis < nowMillis && nowMillis - createMillis > graceMillis;
    }
}

package cc.wechat.observatory.wechat;

public final class OutboxDeliveryPacing {
    private static final long MIN_IDLE_DELAY_MS = 1000L;

    private OutboxDeliveryPacing() {
    }

    public static long nextDelayMs(boolean completedItem, long configuredPollIntervalMs) {
        if (completedItem) {
            return 0L;
        }
        return Math.max(MIN_IDLE_DELAY_MS, configuredPollIntervalMs);
    }
}

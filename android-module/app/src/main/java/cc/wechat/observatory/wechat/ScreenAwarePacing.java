package cc.wechat.observatory.wechat;

/**
 * How fast the phone polls while its screen is off.
 *
 * <p>Message observation is pushed from the WeChat database hook, so an idle
 * phone loses nothing by polling less: the poll loops only reconcile missed
 * rows and drain the outbox. Stretching them while the screen is off keeps the
 * radio quiet and keeps this module's wake pattern close to a normal WeChat
 * install. The WebSocket outbox connection is never paced here, because the
 * server pushes queued sends through it.
 *
 * <p>Long sleeps are served in slices so that a screen-on transition is noticed
 * within {@link #PACE_SLICE_MS} instead of after the whole idle delay.
 */
public final class ScreenAwarePacing {
    /** Never poll faster than this while the screen is off. */
    public static final long SCREEN_OFF_MIN_DELAY_MS = 15_000L;
    /** Longest single sleep; a wake request can cut it short. */
    public static final long PACE_SLICE_MS = 5_000L;

    private boolean screenOn = true;
    private boolean wakeRequested;

    public synchronized void onScreenOn() {
        screenOn = true;
        wakeRequested = true;
    }

    public synchronized void onScreenOff() {
        screenOn = false;
        wakeRequested = false;
    }

    public synchronized boolean isScreenOn() {
        return screenOn;
    }

    /** The requested delay, widened while the screen is off. */
    public synchronized long paceDelay(long baseDelayMs) {
        long base = baseDelayMs < 0L ? 0L : baseDelayMs;
        if (screenOn) {
            return base;
        }
        return Math.max(base, SCREEN_OFF_MIN_DELAY_MS);
    }

    /** Caps a pending sleep so screen transitions are picked up promptly. */
    public static long nextSlice(long remainingMs) {
        if (remainingMs <= 0L) {
            return 0L;
        }
        return Math.min(remainingMs, PACE_SLICE_MS);
    }

    /** True at most once per screen-on transition. */
    public synchronized boolean consumeWakeRequest() {
        boolean requested = wakeRequested;
        wakeRequested = false;
        return requested;
    }

    public synchronized String describe() {
        return "screen=" + (screenOn ? "on" : "off") + " wakePending=" + wakeRequested;
    }
}

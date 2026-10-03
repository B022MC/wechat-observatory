package cc.wechat.observatory.wechat;

/**
 * Whether this installation currently owns its device binding.
 *
 * <p>Several phones may share one API Key. The server accepts one active
 * installation; others are kept on standby. A standby phone claims the binding
 * when the user opens WeChat on it (a foreground transition), and the claim is
 * consumed by the next registration so two phones never take turns stealing it.
 */
public final class DeviceRole {
    public static final String TAKEOVER_FOREGROUND = "foreground";
    /** A foreground claim is only meaningful shortly after the user opened WeChat. */
    public static final long FOREGROUND_CLAIM_WINDOW_MS = 120_000L;
    /** Transient identity read failures keep the current session for this long. */
    public static final long IDENTITY_GRACE_MS = 60_000L;

    public enum State { UNKNOWN, ACTIVE, STANDBY }

    private State state = State.UNKNOWN;
    private long foregroundAt;
    private String activeSummary = "";
    private long identityFailingSince;

    public synchronized State state() {
        return state;
    }

    public synchronized boolean isStandby() {
        return state == State.STANDBY;
    }

    public synchronized String activeSummary() {
        return activeSummary;
    }

    /** The user brought WeChat to the foreground on this phone. */
    public synchronized void onForeground(long now) {
        foregroundAt = now;
    }

    /**
     * Returns the takeover reason to send with the next registration. An active
     * installation consumes the claim immediately: it already owns the binding,
     * and a later displacement must not be undone by an old foreground event.
     */
    public synchronized String takeoverFor(long now) {
        if (foregroundAt <= 0L) {
            return "";
        }
        if (state == State.ACTIVE || now - foregroundAt > FOREGROUND_CLAIM_WINDOW_MS || now < foregroundAt) {
            foregroundAt = 0L;
            return "";
        }
        return TAKEOVER_FOREGROUND;
    }

    /** A registration carrying the claim reached the server (accepted or not). */
    public synchronized void claimSent() {
        foregroundAt = 0L;
    }

    /** Returns true when this phone was not already on standby. */
    public synchronized boolean enterStandby(String summary) {
        boolean changed = state != State.STANDBY;
        state = State.STANDBY;
        activeSummary = summary == null ? "" : summary;
        return changed;
    }

    /** Returns the previous state so callers can react to standby -> active. */
    public synchronized State enterActive() {
        State previous = state;
        state = State.ACTIVE;
        activeSummary = "";
        return previous;
    }

    /** Records a failed identity read; true when the binding should be dropped. */
    public synchronized boolean identityFailed(long now) {
        if (identityFailingSince <= 0L || now < identityFailingSince) {
            identityFailingSince = now;
        }
        return now - identityFailingSince >= IDENTITY_GRACE_MS;
    }

    public synchronized void identityResolved() {
        identityFailingSince = 0L;
    }
}

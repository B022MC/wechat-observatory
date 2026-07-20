package cc.wechat.observatory.wechat;

public final class LocalMessageConfirmation {
    public static final int STATUS_UNKNOWN = Integer.MIN_VALUE;
    public static final int STATUS_SENDING = 1;
    public static final int STATUS_SENT = 2;
    public static final int STATUS_FAILED = 5;

    public enum Result {
        SENT,
        FAILED,
        TIMEOUT
    }

    public interface StatusReader {
        int readStatus() throws Exception;
    }

    public interface Sleeper {
        void sleep(long delayMillis) throws InterruptedException;
    }

    private LocalMessageConfirmation() {
    }

    public static Result awaitTerminal(
            StatusReader reader,
            Sleeper sleeper,
            int maxChecks,
            long delayMillis) throws Exception {
        if (reader == null || sleeper == null) {
            throw new IllegalArgumentException("reader and sleeper are required");
        }
        if (maxChecks < 1) {
            throw new IllegalArgumentException("maxChecks must be positive");
        }
        if (delayMillis < 0L) {
            throw new IllegalArgumentException("delayMillis must not be negative");
        }

        for (int check = 1; check <= maxChecks; check++) {
            int status = reader.readStatus();
            if (status == STATUS_SENT) {
                return Result.SENT;
            }
            if (status == STATUS_FAILED) {
                return Result.FAILED;
            }
            if (check < maxChecks) {
                sleeper.sleep(delayMillis);
            }
        }
        return Result.TIMEOUT;
    }
}

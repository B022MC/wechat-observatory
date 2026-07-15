package cc.wechat.observatory.wechat;

public final class QueueSubmissionRetrier {
    public interface Attempt {
        boolean submit() throws Exception;
    }

    public interface Sleeper {
        void sleep(long delayMillis) throws InterruptedException;
    }

    private QueueSubmissionRetrier() {
    }

    public static boolean awaitAccepted(
            Attempt attempt,
            Sleeper sleeper,
            int maxAttempts,
            long delayMillis) throws Exception {
        if (attempt == null || sleeper == null) {
            throw new IllegalArgumentException("attempt and sleeper are required");
        }
        if (maxAttempts < 1) {
            throw new IllegalArgumentException("maxAttempts must be positive");
        }
        if (delayMillis < 0L) {
            throw new IllegalArgumentException("delayMillis must not be negative");
        }

        for (int attemptNumber = 1; attemptNumber <= maxAttempts; attemptNumber++) {
            if (attempt.submit()) {
                return true;
            }
            if (attemptNumber < maxAttempts) {
                sleeper.sleep(delayMillis);
            }
        }
        return false;
    }
}

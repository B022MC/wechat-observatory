package cc.wechat.observatory.wechat;

import org.junit.Test;

import java.io.IOException;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.fail;

public final class LocalMessageConfirmationTest {
    @Test
    public void confirmsSentAfterSendingState() throws Exception {
        AtomicInteger reads = new AtomicInteger();
        AtomicInteger sleeps = new AtomicInteger();

        LocalMessageConfirmation.Result result = LocalMessageConfirmation.awaitTerminal(
                () -> reads.incrementAndGet() < 3
                        ? LocalMessageConfirmation.STATUS_SENDING
                        : LocalMessageConfirmation.STATUS_SENT,
                delayMillis -> sleeps.incrementAndGet(),
                5,
                500L);

        assertEquals(LocalMessageConfirmation.Result.SENT, result);
        assertEquals(3, reads.get());
        assertEquals(2, sleeps.get());
    }

    @Test
    public void stopsImmediatelyForFailedMessage() throws Exception {
        AtomicInteger sleeps = new AtomicInteger();

        LocalMessageConfirmation.Result result = LocalMessageConfirmation.awaitTerminal(
                () -> LocalMessageConfirmation.STATUS_FAILED,
                delayMillis -> sleeps.incrementAndGet(),
                5,
                500L);

        assertEquals(LocalMessageConfirmation.Result.FAILED, result);
        assertEquals(0, sleeps.get());
    }

    @Test
    public void timesOutAtConfiguredCheckBound() throws Exception {
        AtomicInteger reads = new AtomicInteger();
        AtomicInteger sleeps = new AtomicInteger();

        LocalMessageConfirmation.Result result = LocalMessageConfirmation.awaitTerminal(
                () -> {
                    reads.incrementAndGet();
                    return LocalMessageConfirmation.STATUS_UNKNOWN;
                },
                delayMillis -> sleeps.incrementAndGet(),
                4,
                250L);

        assertEquals(LocalMessageConfirmation.Result.TIMEOUT, result);
        assertEquals(4, reads.get());
        assertEquals(3, sleeps.get());
    }

    @Test
    public void propagatesStatusReadFailure() throws Exception {
        AtomicInteger sleeps = new AtomicInteger();

        try {
            LocalMessageConfirmation.awaitTerminal(
                    () -> {
                        throw new IOException("database unavailable");
                    },
                    delayMillis -> sleeps.incrementAndGet(),
                    5,
                    500L);
            fail("expected IOException");
        } catch (IOException expected) {
            assertEquals("database unavailable", expected.getMessage());
        }

        assertEquals(0, sleeps.get());
    }
}

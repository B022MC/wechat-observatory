package cc.wechat.observatory.wechat;

import org.junit.Test;

import java.io.IOException;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

public final class QueueSubmissionRetrierTest {
    @Test
    public void succeedsWithoutRebuildingAfterBusyResults() throws Exception {
        AtomicInteger calls = new AtomicInteger();
        AtomicInteger sleeps = new AtomicInteger();

        boolean accepted = QueueSubmissionRetrier.awaitAccepted(
                () -> calls.incrementAndGet() == 3,
                delayMillis -> sleeps.incrementAndGet(),
                5,
                500L);

        assertTrue(accepted);
        assertEquals(3, calls.get());
        assertEquals(2, sleeps.get());
    }

    @Test
    public void stopsAtConfiguredAttemptBound() throws Exception {
        AtomicInteger calls = new AtomicInteger();
        AtomicInteger sleeps = new AtomicInteger();

        boolean accepted = QueueSubmissionRetrier.awaitAccepted(
                () -> {
                    calls.incrementAndGet();
                    return false;
                },
                delayMillis -> sleeps.incrementAndGet(),
                4,
                250L);

        assertFalse(accepted);
        assertEquals(4, calls.get());
        assertEquals(3, sleeps.get());
    }

    @Test
    public void stopsImmediatelyWhenSubmissionThrows() throws Exception {
        AtomicInteger calls = new AtomicInteger();
        AtomicInteger sleeps = new AtomicInteger();

        try {
            QueueSubmissionRetrier.awaitAccepted(
                    () -> {
                        calls.incrementAndGet();
                        throw new IOException("queue unavailable");
                    },
                    delayMillis -> sleeps.incrementAndGet(),
                    5,
                    500L);
            fail("expected IOException");
        } catch (IOException expected) {
            assertEquals("queue unavailable", expected.getMessage());
        }

        assertEquals(1, calls.get());
        assertEquals(0, sleeps.get());
    }
}

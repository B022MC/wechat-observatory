package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public final class HistoricalMessageReplayGuardTest {
    @Test
    public void currentMessagesDoNotConsumeTheStaleBudget() {
        HistoricalMessageReplayGuard guard = new HistoricalMessageReplayGuard();
        long now = 1_000_000L;

        assertTrue(guard.allows(now, 999L, 10_000L, 2));
        assertTrue(guard.allows(now, 1_000L, 10_000L, 2));
        assertTrue(guard.allows(now, 1L, 10_000L, 2));
        assertTrue(guard.allows(now, 2L, 10_000L, 2));
        assertFalse(guard.allows(now, 3L, 10_000L, 2));
        assertTrue(guard.allows(now, 1_000L, 10_000L, 2));
    }

    @Test
    public void zeroOrFutureCreationTimeIsNotClassifiedAsHistoricalReplay() {
        HistoricalMessageReplayGuard guard = new HistoricalMessageReplayGuard();
        long now = 1_000_000L;

        assertTrue(guard.allows(now, 0L, 10_000L, 0));
        assertTrue(guard.allows(now, 2_000L, 10_000L, 0));
    }

    @Test
    public void graceBoundaryAndDisabledStaleBudgetAreHandledExplicitly() {
        HistoricalMessageReplayGuard guard = new HistoricalMessageReplayGuard();
        long now = 1_000_000L;

        assertTrue(guard.allows(now, 990L, 10_000L, 0));
        assertFalse(guard.allows(now, 989L, 10_000L, 0));
    }
}

package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertEquals;

import org.junit.Test;

public final class OutboxDeliveryPacingTest {
    @Test
    public void completedAckImmediatelyChecksNextItem() {
        assertEquals(0L, OutboxDeliveryPacing.nextDelayMs(true, 1000L));
        assertEquals(0L, OutboxDeliveryPacing.nextDelayMs(true, 5000L));
    }

    @Test
    public void emptyPollAndFailureKeepAtLeastOneSecondIdleDelay() {
        assertEquals(1000L, OutboxDeliveryPacing.nextDelayMs(false, 0L));
        assertEquals(1000L, OutboxDeliveryPacing.nextDelayMs(false, 500L));
        assertEquals(3000L, OutboxDeliveryPacing.nextDelayMs(false, 3000L));
    }
}

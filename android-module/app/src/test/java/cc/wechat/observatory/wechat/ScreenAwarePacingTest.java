package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public final class ScreenAwarePacingTest {
    @Test
    public void screenOnKeepsTheConfiguredDelay() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        assertTrue(pacing.isScreenOn());
        assertEquals(1000L, pacing.paceDelay(1000L));
        assertEquals(0L, pacing.paceDelay(0L));
    }

    @Test
    public void screenOffWidensAShortDelay() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        pacing.onScreenOff();

        assertFalse(pacing.isScreenOn());
        assertEquals(ScreenAwarePacing.SCREEN_OFF_MIN_DELAY_MS, pacing.paceDelay(1000L));
    }

    @Test
    public void screenOffNeverShortensALongerDelay() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        pacing.onScreenOff();

        assertEquals(60000L, pacing.paceDelay(60000L));
    }

    @Test
    public void negativeDelayIsTreatedAsZeroOnScreenAndAsTheMinimumWhenIdle() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        assertEquals(0L, pacing.paceDelay(-5L));

        pacing.onScreenOff();

        assertEquals(ScreenAwarePacing.SCREEN_OFF_MIN_DELAY_MS, pacing.paceDelay(-5L));
    }

    @Test
    public void screenOnRaisesExactlyOneWakeRequest() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        pacing.onScreenOff();
        assertFalse(pacing.consumeWakeRequest());

        pacing.onScreenOn();
        assertTrue(pacing.consumeWakeRequest());
        assertFalse(pacing.consumeWakeRequest());
    }

    @Test
    public void screenOffDropsAPendingWakeRequest() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        pacing.onScreenOn();
        pacing.onScreenOff();

        assertFalse(pacing.consumeWakeRequest());
    }

    @Test
    public void slicesCapLongSleepsSoAWakeIsNoticedEarly() {
        assertEquals(0L, ScreenAwarePacing.nextSlice(0L));
        assertEquals(0L, ScreenAwarePacing.nextSlice(-1L));
        assertEquals(1200L, ScreenAwarePacing.nextSlice(1200L));
        assertEquals(ScreenAwarePacing.PACE_SLICE_MS, ScreenAwarePacing.nextSlice(60000L));
    }

    @Test
    public void describeReportsScreenAndPendingWake() {
        ScreenAwarePacing pacing = new ScreenAwarePacing();

        pacing.onScreenOff();
        assertTrue(pacing.describe().contains("screen=off"));

        pacing.onScreenOn();
        assertTrue(pacing.describe().contains("screen=on"));
        assertTrue(pacing.describe().contains("wakePending=true"));
    }
}

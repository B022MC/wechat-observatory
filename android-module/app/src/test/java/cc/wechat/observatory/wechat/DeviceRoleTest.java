package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class DeviceRoleTest {
    @Test
    public void coldStartForegroundClaimsUntilSent() {
        DeviceRole role = new DeviceRole();
        role.onForeground(1_000L);
        assertEquals(DeviceRole.TAKEOVER_FOREGROUND, role.takeoverFor(2_000L));
        assertEquals(DeviceRole.TAKEOVER_FOREGROUND, role.takeoverFor(3_000L));
        role.claimSent();
        assertEquals("", role.takeoverFor(4_000L));
    }

    @Test
    public void activePhoneConsumesForegroundSoLaterDisplacementSticks() {
        DeviceRole role = new DeviceRole();
        role.enterActive();
        role.onForeground(1_000L);
        assertEquals("", role.takeoverFor(1_500L));
        assertTrue(role.enterStandby("Model B"));
        assertEquals("", role.takeoverFor(2_000L));
        assertTrue(role.isStandby());
        assertEquals("Model B", role.activeSummary());
    }

    @Test
    public void standbyClaimExpires() {
        DeviceRole role = new DeviceRole();
        role.enterStandby("other");
        role.onForeground(10_000L);
        assertEquals(DeviceRole.TAKEOVER_FOREGROUND, role.takeoverFor(10_000L + DeviceRole.FOREGROUND_CLAIM_WINDOW_MS));
        assertEquals("", role.takeoverFor(10_001L + DeviceRole.FOREGROUND_CLAIM_WINDOW_MS));
        assertEquals("", role.takeoverFor(10_002L + DeviceRole.FOREGROUND_CLAIM_WINDOW_MS));
    }

    @Test
    public void transitionsReportPreviousState() {
        DeviceRole role = new DeviceRole();
        assertEquals(DeviceRole.State.UNKNOWN, role.enterActive());
        assertTrue(role.enterStandby("x"));
        assertFalse(role.enterStandby("y"));
        assertEquals(DeviceRole.State.STANDBY, role.enterActive());
        assertEquals("", role.activeSummary());
    }

    @Test
    public void transientIdentityFailuresKeepBindingWithinGrace() {
        DeviceRole role = new DeviceRole();
        assertFalse(role.identityFailed(1_000L));
        assertFalse(role.identityFailed(1_000L + DeviceRole.IDENTITY_GRACE_MS - 1));
        assertTrue(role.identityFailed(1_000L + DeviceRole.IDENTITY_GRACE_MS));
        role.identityResolved();
        assertFalse(role.identityFailed(500_000L));
    }
}

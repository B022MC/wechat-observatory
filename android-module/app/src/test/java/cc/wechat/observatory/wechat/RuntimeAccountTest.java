package cc.wechat.observatory.wechat;

import org.junit.Test;

import static org.junit.Assert.*;

public class RuntimeAccountTest {
    private RuntimeAccount account(String owner, String directory) {
        return new RuntimeAccount(owner, "", "/data/user/0/com.tencent.mm/MicroMsg/" + directory + "/EnMicroMsg.db");
    }

    @Test
    public void accountSwitchResetsPollingAndSnapshotTimers() {
        RuntimeAccount old = account("wxid_lanlan", "old");
        old.advanceWatermark(50000);
        old.lastContactSyncAt = 12345;
        old.lastMessagePollAt = 12345;
        RuntimeAccount current = account("wxid_yuyu", "current");
        assertFalse(current.sameLogin(old));
        assertFalse(current.watermarkReady());
        assertEquals(0, current.lastContactSyncAt);
        assertEquals(0, current.lastMessagePollAt);
        current.initializeWatermark(20);
        old.advanceWatermark(50001); // Late work for the old account stays isolated.
        assertEquals(20, current.messageWatermark());
    }

    @Test
    public void rejectsSiblingDirectoriesAndTraversal() {
        RuntimeAccount current = account("wxid_yuyu", "current");
        assertTrue(current.ownsDatabase("/data/data/com.tencent.mm/MicroMsg/current/EnMicroMsg.db"));
        assertTrue(current.ownsDatabase("/data/user/0/com.tencent.mm/MicroMsg/current/contacts.db"));
        assertFalse(current.ownsDatabase("/data/user/0/com.tencent.mm/MicroMsg/current-old/EnMicroMsg.db"));
        assertFalse(current.ownsDatabase("/data/user/0/com.tencent.mm/MicroMsg/current/../old/EnMicroMsg.db"));
        assertFalse(current.ownsDatabase("/data/user/10/com.tencent.mm/MicroMsg/current/EnMicroMsg.db"));
    }

    @Test
    public void sameNameInAnotherDirectoryIsAnotherLogin() {
        assertFalse(account("wxid_yuyu", "one").sameLogin(account("wxid_yuyu", "two")));
        assertTrue(account("wxid_yuyu", "one").sameLogin(account("wxid_yuyu", "one")));
    }

    @Test
    public void watermarkNeverMovesBackwards() {
        RuntimeAccount current = account("wxid_yuyu", "current");
        current.advanceWatermark(25);
        current.initializeWatermark(20);
        current.advanceWatermark(22);
        assertEquals(25, current.messageWatermark());
    }

    @Test
    public void replayBudgetIsIndependentForEachAccount() {
        RuntimeAccount old = account("wxid_lanlan", "old");
        RuntimeAccount current = account("wxid_yuyu", "current");
        assertTrue(old.replayGuard.allows(1000000, 1, 0, 1));
        assertFalse(old.replayGuard.allows(1000000, 1, 0, 1));
        assertTrue(current.replayGuard.allows(1000000, 1, 0, 1));
    }

    @Test(expected = IllegalArgumentException.class)
    public void rejectsDirectoryHashAsLoginIdentity() {
        account("acct_abcdef", "current");
    }
}

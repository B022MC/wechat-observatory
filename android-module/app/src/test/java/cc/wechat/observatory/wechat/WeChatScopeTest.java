package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public final class WeChatScopeTest {
    private static final int MAIN_UID = 10234;          // user 0
    private static final int XIAOMI_CLONE_UID = 99910234; // user 999 (Xiaomi dual apps)
    private static final int WORK_PROFILE_UID = 1010234;  // user 10

    @Test
    public void unknownOrMissingScopeServesOnlyTheMainWeChat() {
        assertEquals(WeChatScope.MAIN, WeChatScope.normalize(null));
        assertEquals(WeChatScope.MAIN, WeChatScope.normalize(""));
        assertEquals(WeChatScope.MAIN, WeChatScope.normalize("both"));
        assertEquals(WeChatScope.CLONE, WeChatScope.normalize(" Clone "));
        assertEquals(WeChatScope.ALL, WeChatScope.normalize("ALL"));
    }

    @Test
    public void androidUserComesFromTheProcessUid() {
        assertEquals(0, WeChatScope.androidUserId(MAIN_UID));
        assertEquals(999, WeChatScope.androidUserId(XIAOMI_CLONE_UID));
        assertEquals(10, WeChatScope.androidUserId(WORK_PROFILE_UID));
        assertEquals(0, WeChatScope.androidUserId(-1));
        assertFalse(WeChatScope.isCloneUid(MAIN_UID));
        assertTrue(WeChatScope.isCloneUid(XIAOMI_CLONE_UID));
        assertTrue(WeChatScope.isCloneUid(WORK_PROFILE_UID));
    }

    @Test
    public void mainScopeIgnoresTheClone() {
        assertTrue(WeChatScope.serves(WeChatScope.MAIN, MAIN_UID));
        assertFalse(WeChatScope.serves(WeChatScope.MAIN, XIAOMI_CLONE_UID));
        assertFalse(WeChatScope.serves(null, XIAOMI_CLONE_UID));
    }

    @Test
    public void cloneScopeIgnoresTheMainWeChat() {
        assertFalse(WeChatScope.serves(WeChatScope.CLONE, MAIN_UID));
        assertTrue(WeChatScope.serves(WeChatScope.CLONE, XIAOMI_CLONE_UID));
        assertTrue(WeChatScope.serves(WeChatScope.CLONE, WORK_PROFILE_UID));
    }

    @Test
    public void allScopeKeepsTheOldBehaviour() {
        assertTrue(WeChatScope.serves(WeChatScope.ALL, MAIN_UID));
        assertTrue(WeChatScope.serves(WeChatScope.ALL, XIAOMI_CLONE_UID));
    }

    @Test
    public void describeNamesTheInstance() {
        assertEquals("main/user0", WeChatScope.describe(MAIN_UID));
        assertEquals("clone/user999", WeChatScope.describe(XIAOMI_CLONE_UID));
    }
}

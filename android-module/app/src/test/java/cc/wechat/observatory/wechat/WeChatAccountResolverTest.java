package cc.wechat.observatory.wechat;

import org.junit.Test;
import static org.junit.Assert.*;

public class WeChatAccountResolverTest {
    public static class Config78 {
        String id = "wxid_yuyu";
        String alias = "liuhhdsh";
        public Object m(int key, Object fallback) {
            if (key == 2) return id;
            if (key == 42) return alias;
            if (key == 4) return "雨雨";
            return fallback;
        }
    }

    public static class Config74 {
        public Object l(int key, Object fallback) {
            if (key == 2) return "a565265501";
            if (key == 4) return "兰兰";
            return fallback;
        }
    }

    public static class Storage {
        final Object config;
        String path = "/data/user/0/com.tencent.mm/MicroMsg/current/EnMicroMsg.db";
        Storage(Object config) { this.config = config; }
        public String g() { return path; }
        public Object c() { return config; }
    }

    @Test
    public void readsCurrentMmkvConfigurationWithoutSqlUserinfo() throws Exception {
        RuntimeAccount account = WeChatAccountResolver.readStorage(new Storage(new Config78()), "m");
        assertEquals("wxid_yuyu", account.wxid);
        assertEquals("雨雨", account.nickname);
    }

    @Test
    public void preservesOldVersionAndNonWxidAccountNames() throws Exception {
        RuntimeAccount account = WeChatAccountResolver.readStorage(new Storage(new Config74()), "l");
        assertEquals("a565265501", account.wxid);
        assertEquals("兰兰", account.nickname);
    }

    @Test(expected = IllegalStateException.class)
    public void waitsForCanonicalIdentityEvenWhenAliasExists() throws Exception {
        Config78 config = new Config78(); config.id = "";
        WeChatAccountResolver.readStorage(new Storage(config), "m");
    }

    @Test(expected = IllegalStateException.class)
    public void absentIdentityDoesNotInventDirectoryOwner() throws Exception {
        Config78 config = new Config78(); config.id = ""; config.alias = "";
        WeChatAccountResolver.readStorage(new Storage(config), "m");
    }

    @Test(expected = IllegalStateException.class)
    public void rejectsAccountChangeDuringConfigRead() throws Exception {
        Storage storage = new Storage(new Config78()) {
            int reads;
            @Override public String g() {
                return ++reads == 1 ? path : path.replace("/current/", "/next/");
            }
        };
        WeChatAccountResolver.readStorage(storage, "m");
    }

    @Test
    public void displayNamesAndNumericValuesAreNotAccountIdentifiers() {
        assertFalse(WeChatAccountResolver.isAccountId("雨雨"));
        assertFalse(WeChatAccountResolver.isAccountId("12345"));
        assertFalse(WeChatAccountResolver.isAccountId("acct_123abc"));
        assertTrue(WeChatAccountResolver.isAccountId("wxid_current"));
    }
}

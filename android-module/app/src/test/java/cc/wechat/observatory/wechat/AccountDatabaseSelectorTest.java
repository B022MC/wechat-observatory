package cc.wechat.observatory.wechat;

import org.junit.Test;
import java.util.ArrayList;
import java.util.List;
import static org.junit.Assert.*;

public class AccountDatabaseSelectorTest {
    private final RuntimeAccount account = new RuntimeAccount("wxid_current", "雨雨",
            "/data/user/0/com.tencent.mm/MicroMsg/current/EnMicroMsg.db");

    private static class Db {
        final String path;
        final boolean readable;
        Db(String directory, String name, boolean readable) {
            this.path = "/data/user/0/com.tencent.mm/MicroMsg/" + directory + "/" + name;
            this.readable = readable;
        }
    }

    private final List<Db> probed = new ArrayList<>();
    private final AccountDatabaseSelector.Probe probe = new AccountDatabaseSelector.Probe() {
        public String path(Object db) { return ((Db) db).path; }
        public boolean supports(Object db) {
            probed.add((Db) db);
            return ((Db) db).readable;
        }
    };

    @Test
    public void selectsCurrentDatabaseEvenWhenItsContactTableIsEmpty() {
        Db oldWithManyContacts = new Db("old", "EnMicroMsg.db", true);
        Db currentWithEmptyTable = new Db("current", "EnMicroMsg.db", true);
        assertSame(currentWithEmptyTable, AccountDatabaseSelector.select(account,
                new Object[]{oldWithManyContacts, currentWithEmptyTable}, probe));
        assertFalse(probed.contains(oldWithManyContacts));
    }

    @Test
    public void unreadableCurrentAccountNeverFallsBackToOldAccount() {
        Db old = new Db("old", "EnMicroMsg.db", true);
        Db current = new Db("current", "EnMicroMsg.db", false);
        assertNull(AccountDatabaseSelector.select(account, new Object[]{old, current}, probe));
        assertFalse(probed.contains(old));
    }

    @Test
    public void mayUseSplitTableStorageOnlyInsideTheCurrentAccount() {
        Db foreign = new Db("old", "contacts.db", true);
        Db currentMain = new Db("current", "EnMicroMsg.db", false);
        Db currentContacts = new Db("current", "contacts.db", true);
        assertSame(currentContacts, AccountDatabaseSelector.select(account,
                new Object[]{foreign, currentContacts, currentMain}, probe));
        assertSame(currentMain, probed.get(0));
        assertFalse(probed.contains(foreign));
    }

    @Test
    public void noCurrentAccountNeverSelectsAnyDatabase() {
        assertNull(AccountDatabaseSelector.select(null,
                new Object[]{new Db("old", "EnMicroMsg.db", true)}, probe));
        assertTrue(probed.isEmpty());
    }
}

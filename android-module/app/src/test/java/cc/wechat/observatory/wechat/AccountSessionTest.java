package cc.wechat.observatory.wechat;

import android.content.SharedPreferences;
import java.lang.reflect.Proxy;
import java.util.HashMap;
import java.util.Map;
import org.junit.Test;
import static org.junit.Assert.*;

public class AccountSessionTest {
    private SharedPreferences preferences(Map<String, Object> disk, boolean writable) {
        return (SharedPreferences) Proxy.newProxyInstance(getClass().getClassLoader(),
                new Class<?>[]{SharedPreferences.class}, (proxy, method, args) -> {
                    if (method.getName().equals("getString") || method.getName().equals("getLong")) {
                        return disk.containsKey(args[0]) ? disk.get(args[0]) : args[1];
                    }
                    if (method.getName().equals("edit")) {
                        Map<String, Object> pending = new HashMap<>();
                        return Proxy.newProxyInstance(getClass().getClassLoader(),
                                new Class<?>[]{SharedPreferences.Editor.class}, (editor, operation, values) -> {
                                    if (operation.getName().equals("putString") || operation.getName().equals("putLong")) {
                                        pending.put((String) values[0], values[1]); return editor;
                                    }
                                    if (operation.getName().equals("commit")) {
                                        if (writable) disk.putAll(pending);
                                        return writable;
                                    }
                                    throw new AssertionError(operation.getName());
                                });
                    }
                    throw new AssertionError(method.getName());
                });
    }

    @Test public void restartAndAccountRoundTripKeepInstallationAndAdvanceGeneration() {
        Map<String, Object> disk = new HashMap<>();
        AccountSession a = AccountSession.allocate(preferences(disk, true));
        AccountSession b = AccountSession.allocate(preferences(disk, true));
        AccountSession returnedA = AccountSession.allocate(preferences(disk, true));
        assertEquals(a.instanceId, b.instanceId);
        assertEquals(a.instanceId, returnedA.instanceId);
        assertEquals(1, a.generation);
        assertEquals(2, b.generation);
        assertEquals(3, returnedA.generation);
        assertNotEquals(a.id, returnedA.id);
        assertFalse(returnedA.acceptsEcho(a.id, a.generation));
        assertFalse(a.acceptsEcho("", 0)); // An old server does not establish a session.
        assertFalse(a.acceptsEcho(a.id, 2));
        assertTrue(a.acceptsEcho(a.id, 1));
    }

    @Test(expected = IllegalStateException.class)
    public void failedDiskCommitNeverProducesAnUploadSession() {
        AccountSession.allocate(preferences(new HashMap<>(), false));
    }

    @Test(expected = IllegalStateException.class)
    public void sequenceNeverWraps() {
        Map<String, Object> disk = new HashMap<>(); disk.put("generation", Long.MAX_VALUE);
        AccountSession.allocate(preferences(disk, true));
    }
}

package cc.wechat.observatory.wechat;

import android.content.Context;
import android.content.SharedPreferences;
import java.util.UUID;

/** Allocated once per runtime binding, before any network operation. */
public final class AccountSession {
    public final String instanceId;
    public final String id;
    public final long generation;

    public AccountSession(String instanceId, String id, long generation) {
        if (instanceId == null || id == null || generation <= 0) {
            throw new IllegalArgumentException("Invalid account session");
        }
        this.instanceId = instanceId;
        this.id = id;
        this.generation = generation;
    }

    public boolean acceptsEcho(String session, long generation) {
        return id.equals(session) && this.generation == generation;
    }

    public static synchronized AccountSession allocate(Context context) {
        return allocate(context.getSharedPreferences("observatory_account_sessions", Context.MODE_PRIVATE));
    }

    static synchronized AccountSession allocate(SharedPreferences prefs) {
        String instance = prefs.getString("instance", "");
        if (instance.isEmpty()) instance = UUID.randomUUID().toString();
        long previous = prefs.getLong("generation", 0L);
        if (previous < 0 || previous == Long.MAX_VALUE) {
            throw new IllegalStateException("Account generation exhausted");
        }
        long next = previous + 1;
        // Synchronous commit makes process death/restart strictly advance the sequence.
        if (!prefs.edit().putString("instance", instance).putLong("generation", next).commit()) {
            throw new IllegalStateException("Account session persistence failed");
        }
        return new AccountSession(instance, UUID.randomUUID().toString(), next);
    }
}

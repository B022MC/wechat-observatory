package cc.wechat.observatory;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.content.Context;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;

import java.util.Map;

public final class BridgeConfigProvider extends ContentProvider {
    static final String PREFS_NAME = "bridge_config";
    static final String AUTHORITY = "cc.wechat.observatory.config";
    static final String[] CONFIG_KEYS = new String[]{
            "enabled",
            "bridge_url",
            "api_key",
            "poll_interval_ms",
            "poll_limit",
            "contact_sync_interval_ms",
            "contact_sync_limit",
            "contact_include_chatrooms",
            "media_upload_enabled",
            "media_upload_limit_bytes"
    };

    @Override
    public boolean onCreate() {
        return true;
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection, String[] selectionArgs, String sortOrder) {
        Context context = getContext();
        MatrixCursor cursor = new MatrixCursor(new String[]{"key", "value"});
        if (context == null) {
            return cursor;
        }

        SharedPreferences prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE);
        Map<String, ?> all = prefs.getAll();
        for (String key : CONFIG_KEYS) {
            Object value = all.get(key);
            if (value != null) {
                cursor.addRow(new Object[]{key, String.valueOf(value)});
            }
        }
        return cursor;
    }

    @Override
    public String getType(Uri uri) {
        return "vnd.android.cursor.dir/vnd.cc.wechat.observatory.config";
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) {
        return 0;
    }
}

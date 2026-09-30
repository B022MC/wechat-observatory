package cc.wechat.observatory.wechat;

import java.io.File;
import java.io.IOException;

/** A verified login and its own polling state; database handles never cross this scope. */
public final class RuntimeAccount {
    public String registrationTarget;
    public AccountSession session; // Assigned before publishing CURRENT_ACCOUNT.
    public final String wxid;
    public final String nickname;
    public final String mainDatabasePath;
    public final String directory;
    public volatile Object contactDatabase;
    public volatile Object messageDatabase;
    public volatile long lastContactSyncAt;
    public volatile long lastMessagePollAt;
    public final HistoricalMessageReplayGuard replayGuard = new HistoricalMessageReplayGuard();
    public volatile boolean replayLimitLogged;
    private long lastMessageId;
    private boolean watermarkReady;

    public RuntimeAccount(String wxid, String nickname, String mainDatabasePath) {
        this.wxid = wxid;
        this.nickname = nickname == null ? "" : nickname;
        this.mainDatabasePath = normalizePath(mainDatabasePath);
        if (wxid == null || wxid.trim().isEmpty() || wxid.startsWith("acct_")
                || !this.mainDatabasePath.endsWith("/EnMicroMsg.db")) {
            throw new IllegalArgumentException("Current WeChat account is not ready");
        }
        this.directory = this.mainDatabasePath.substring(
                0, this.mainDatabasePath.lastIndexOf('/') + 1);
    }

    public boolean sameLogin(RuntimeAccount other) {
        return other != null && wxid.equals(other.wxid) && directory.equals(other.directory);
    }

    public boolean ownsDatabase(String path) {
        String normalized = normalizePath(path);
        return !normalized.isEmpty() && normalized.startsWith(directory);
    }

    public synchronized boolean watermarkReady() {
        return watermarkReady;
    }

    public synchronized long messageWatermark() {
        return lastMessageId;
    }

    public synchronized void initializeWatermark(long id) {
        if (!watermarkReady) {
            lastMessageId = Math.max(0L, id);
            watermarkReady = true;
        }
    }

    public synchronized void advanceWatermark(long id) {
        lastMessageId = Math.max(lastMessageId, id);
        watermarkReady = true;
    }

    public static String normalizePath(String path) {
        if (path == null || path.trim().isEmpty()) {
            return "";
        }
        // Android's app-data alias can appear differently in the kernel and WCDB.
        String value = path.replace("/data/data/com.tencent.mm/", "/data/user/0/com.tencent.mm/");
        try {
            return new File(value).getCanonicalPath().replace('\\', '/');
        } catch (IOException e) {
            return "";
        }
    }
}

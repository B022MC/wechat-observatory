package cc.wechat.observatory;

import cc.wechat.observatory.config.BridgeConfig;

final class ContactSnapshotPlan {
    static final String CONTACT_QUERY = ""
            + "SELECT username,nickname,conRemark,alias,type,verifyFlag "
            + "FROM rcontact "
            + "WHERE username IS NOT NULL AND username <> ''";

    private static final int MAX_LIMIT = 10000;

    private ContactSnapshotPlan() {
    }

    static int outputLimit(int configuredLimit) {
        if (configuredLimit <= 0) {
            return BridgeConfig.DEFAULT_CONTACT_SYNC_LIMIT;
        }
        return Math.min(configuredLimit, MAX_LIMIT);
    }
}

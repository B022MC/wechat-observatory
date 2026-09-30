package cc.wechat.observatory.wechat;

/** Table capability is considered only after the kernel has established account ownership. */
public final class AccountDatabaseSelector {
    public interface Probe {
        String path(Object database);
        boolean supports(Object database);
    }

    private AccountDatabaseSelector() {
    }

    public static Object select(RuntimeAccount account, Object[] databases, Probe probe) {
        if (account == null || databases == null) {
            return null;
        }
        for (int pass = 0; pass < 2; pass++) {
            for (Object database : databases) {
                if (database == null) {
                    continue;
                }
                String path = probe.path(database);
                boolean main = account.mainDatabasePath.equals(RuntimeAccount.normalizePath(path));
                if (!account.ownsDatabase(path) || (pass == 0) != main) {
                    continue;
                }
                if (probe.supports(database)) {
                    return database;
                }
            }
        }
        return null;
    }
}

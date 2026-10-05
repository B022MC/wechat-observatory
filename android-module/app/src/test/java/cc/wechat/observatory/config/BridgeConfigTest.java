package cc.wechat.observatory.config;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotEquals;

import java.util.Properties;

import org.junit.Test;

import cc.wechat.observatory.gateway.GatewayEndpoint;
import cc.wechat.observatory.wechat.HookTargets;
import cc.wechat.observatory.wechat.WeChatScope;

public final class BridgeConfigTest {
    @Test
    public void configuredBridgeUrlOverridesDefaultEndpoint() {
        Properties properties = new Properties();
        properties.setProperty("bridge_url", "http://192.168.1.10:8088");
        properties.setProperty("api_key", "test-key");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals("http://192.168.1.10:8088", config.baseUrl);
        assertEquals("test-key", config.apiKey);
    }

    @Test
    public void missingBridgeUrlUsesDefaultEndpoint() {
        BridgeConfig config = BridgeConfig.fromProperties(new Properties());

        assertEquals(GatewayEndpoint.PRODUCTION_BASE_URL, config.baseUrl);
        assertEquals(false, config.outboxWebSocketEnabled);
    }

    @Test
    public void outboxWebSocketCanBeEnabledExplicitly() {
        Properties properties = new Properties();
        properties.setProperty("outbox_websocket_enabled", "1");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals(true, config.outboxWebSocketEnabled);
    }

    @Test
    public void invalidBridgeUrlUsesDefaultEndpoint() {
        Properties properties = new Properties();
        properties.setProperty("bridge_url", "not-a-url");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals(GatewayEndpoint.PRODUCTION_BASE_URL, config.baseUrl);
    }

    @Test
    public void bridgeUrlChangeUpdatesConfigSignature() {
        Properties first = new Properties();
        first.setProperty("bridge_url", "https://47.108.171.42/observatory");
        Properties second = new Properties();
        second.setProperty("bridge_url", "https://47.108.232.203/observatory");

        assertNotEquals(
                BridgeConfig.fromProperties(first).signature,
                BridgeConfig.fromProperties(second).signature);
    }

    @Test
    public void contactSnapshotDefaultsToMaximumAcceptedSize() {
        BridgeConfig config = BridgeConfig.fromProperties(new Properties());

        assertEquals(10000, BridgeConfig.DEFAULT_CONTACT_SYNC_LIMIT);
        assertEquals(10000, config.contactSyncLimit);
    }

    @Test
    public void staleMessageReplayDefaultsAreBoundedAndConfigurable() {
        Properties properties = new Properties();
        properties.setProperty("stale_message_grace_ms", "300000");
        properties.setProperty("stale_message_replay_limit", "25");

        BridgeConfig configured = BridgeConfig.fromProperties(properties);
        BridgeConfig defaults = BridgeConfig.fromProperties(new Properties());

        assertEquals(300000L, configured.staleMessageGraceMs);
        assertEquals(25, configured.staleMessageReplayLimit);
        assertEquals(900000L, defaults.staleMessageGraceMs);
        assertEquals(500, defaults.staleMessageReplayLimit);
    }

    @Test
    public void invalidStaleMessageReplayLimitFallsBackToTheDefault() {
        Properties properties = new Properties();
        properties.setProperty("stale_message_replay_limit", "2147483648");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals(BridgeConfig.DEFAULT_STALE_MESSAGE_REPLAY_LIMIT, config.staleMessageReplayLimit);
    }

    @Test
    public void hookTargetsResolveAgainstTheRunningWeChatBuild() {
        Properties properties = new Properties();
        properties.setProperty("hook_override_bind", "8.0.76/3141");
        properties.setProperty("hook_observation_class", "com.tencent.wcdb.database.SQLiteDatabaseX");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals("com.tencent.wcdb.database.SQLiteDatabaseX",
                config.hookTargets("8.0.76/3141").observationClass);
        assertEquals(HookTargets.DEFAULT_OBSERVATION_CLASS,
                config.hookTargets("8.0.78/3180").observationClass);
        assertEquals("bind-mismatch", config.hookTargets("8.0.78/3180").rejection);
    }

    @Test
    public void hookTargetOverridesArePartOfTheConfigSignature() {
        Properties first = new Properties();
        first.setProperty("hook_observation_class", "com.tencent.wcdb.database.SQLiteDatabaseA");
        Properties second = new Properties();
        second.setProperty("hook_observation_class", "com.tencent.wcdb.database.SQLiteDatabaseB");

        assertNotEquals(
                BridgeConfig.fromProperties(first).signature,
                BridgeConfig.fromProperties(second).signature);
    }

    @Test
    public void configWithoutHookKeysUsesBuiltInTargets() {
        BridgeConfig config = BridgeConfig.fromProperties(new Properties());

        assertEquals(HookTargets.DEFAULT_APP_CLASS, config.hookTargets("8.0.76/3141").appClass);
        assertEquals("", config.hookTargets("8.0.76/3141").rejection);
    }

    @Test
    public void wechatScopeDefaultsToTheMainWeChat() {
        assertEquals(WeChatScope.MAIN, BridgeConfig.fromProperties(new Properties()).wechatScope);
        Properties clone = new Properties();
        clone.setProperty("wechat_scope", "clone");
        assertEquals(WeChatScope.CLONE, BridgeConfig.fromProperties(clone).wechatScope);
        Properties invalid = new Properties();
        invalid.setProperty("wechat_scope", "everything");
        assertEquals(WeChatScope.MAIN, BridgeConfig.fromProperties(invalid).wechatScope);
    }

    @Test
    public void wechatScopeIsPartOfTheConfigSignature() {
        Properties main = new Properties();
        main.setProperty("wechat_scope", "main");
        Properties clone = new Properties();
        clone.setProperty("wechat_scope", "clone");

        assertNotEquals(
                BridgeConfig.fromProperties(main).signature,
                BridgeConfig.fromProperties(clone).signature);
    }

    @Test
    public void unselectedWeChatInstanceIsDisabled() {
        int mainUid = 10234;
        int cloneUid = 99910234;
        Properties properties = new Properties();
        properties.setProperty("api_key", "test-key");

        BridgeConfig mainInMain = BridgeConfig.fromProperties(properties);
        BridgeConfig.applyWeChatScope(mainInMain, mainUid);
        assertEquals(true, mainInMain.enabled);
        assertEquals(false, mainInMain.scopeExcluded);

        BridgeConfig mainInClone = BridgeConfig.fromProperties(properties);
        BridgeConfig.applyWeChatScope(mainInClone, cloneUid);
        assertEquals(false, mainInClone.enabled);
        assertEquals(true, mainInClone.scopeExcluded);

        properties.setProperty("wechat_scope", "clone");
        BridgeConfig cloneInMain = BridgeConfig.fromProperties(properties);
        BridgeConfig.applyWeChatScope(cloneInMain, mainUid);
        assertEquals(false, cloneInMain.enabled);
        BridgeConfig cloneInClone = BridgeConfig.fromProperties(properties);
        BridgeConfig.applyWeChatScope(cloneInClone, cloneUid);
        assertEquals(true, cloneInClone.enabled);

        properties.setProperty("wechat_scope", "main");
        properties.setProperty("enabled", "0");
        BridgeConfig disabled = BridgeConfig.fromProperties(properties);
        BridgeConfig.applyWeChatScope(disabled, mainUid);
        assertEquals(false, disabled.enabled);
        assertEquals(false, disabled.scopeExcluded);
    }
}

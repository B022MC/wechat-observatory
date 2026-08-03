package cc.wechat.observatory.config;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotEquals;

import java.util.Properties;

import org.junit.Test;

import cc.wechat.observatory.gateway.GatewayEndpoint;

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
}

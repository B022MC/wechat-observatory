package cc.wechat.observatory.config;

import static org.junit.Assert.assertEquals;

import java.util.Properties;

import org.junit.Test;

import cc.wechat.observatory.gateway.GatewayEndpoint;

public final class BridgeConfigTest {
    @Test
    public void legacyBridgeUrlCannotOverrideProductionEndpoint() {
        Properties properties = new Properties();
        properties.setProperty("bridge_url", "http://192.168.1.10:8088");
        properties.setProperty("api_key", "test-key");

        BridgeConfig config = BridgeConfig.fromProperties(properties);

        assertEquals(GatewayEndpoint.PRODUCTION_BASE_URL, config.baseUrl);
        assertEquals("test-key", config.apiKey);
    }

    @Test
    public void contactSnapshotDefaultsToMaximumAcceptedSize() {
        BridgeConfig config = BridgeConfig.fromProperties(new Properties());

        assertEquals(10000, BridgeConfig.DEFAULT_CONTACT_SYNC_LIMIT);
        assertEquals(10000, config.contactSyncLimit);
    }
}

package cc.wechat.observatory.gateway;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.net.MalformedURLException;

import org.junit.Test;

public final class GatewayEndpointTest {
    @Test
    public void httpsDefaultsTo443AndKeepsBasePath() throws Exception {
        GatewayEndpoint endpoint = GatewayEndpoint.parse("https://47.108.171.42/observatory/");

        assertTrue(endpoint.isTls());
        assertEquals(443, endpoint.port());
        assertEquals("47.108.171.42", endpoint.hostHeader());
        assertEquals("/observatory/module/outbox/ws?device=phone-a",
                endpoint.requestPath("/module/outbox/ws?device=phone-a"));
    }

    @Test
    public void httpKeepsExplicitPort() throws Exception {
        GatewayEndpoint endpoint = GatewayEndpoint.parse("http://192.168.1.10:8088");

        assertFalse(endpoint.isTls());
        assertEquals(8088, endpoint.port());
        assertEquals("192.168.1.10:8088", endpoint.hostHeader());
        assertEquals("/module/register", endpoint.requestPath("module/register"));
    }

    @Test(expected = MalformedURLException.class)
    public void rejectsQueryInBaseUrl() throws Exception {
        GatewayEndpoint.parse("https://example.test/observatory?token=bad");
    }
}

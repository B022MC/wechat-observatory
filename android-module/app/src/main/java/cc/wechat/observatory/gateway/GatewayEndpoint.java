package cc.wechat.observatory.gateway;

import java.net.MalformedURLException;
import java.net.URL;
import java.util.Locale;

/** Parses the configured gateway origin once so HTTP and WebSocket share URL rules. */
public final class GatewayEndpoint {
    private final URL baseUrl;
    private final String base;
    private final boolean tls;
    private final int port;

    private GatewayEndpoint(URL baseUrl) {
        this.baseUrl = baseUrl;
        this.tls = "https".equalsIgnoreCase(baseUrl.getProtocol());
        this.port = baseUrl.getPort() > 0 ? baseUrl.getPort() : baseUrl.getDefaultPort();
        String value = baseUrl.toExternalForm();
        while (value.endsWith("/") && value.length() > baseUrl.getProtocol().length() + 3) {
            value = value.substring(0, value.length() - 1);
        }
        this.base = value;
    }

    public static GatewayEndpoint parse(String value) throws MalformedURLException {
        if (value == null || value.trim().isEmpty()) {
            throw new MalformedURLException("bridge URL is empty");
        }
        URL url = new URL(value.trim());
        String protocol = url.getProtocol().toLowerCase(Locale.US);
        if (!("http".equals(protocol) || "https".equals(protocol))) {
            throw new MalformedURLException("bridge URL must use http or https");
        }
        if (url.getHost() == null || url.getHost().isEmpty()) {
            throw new MalformedURLException("bridge URL host is empty");
        }
        if (url.getQuery() != null || url.getRef() != null) {
            throw new MalformedURLException("bridge URL must not contain a query or fragment");
        }
        return new GatewayEndpoint(url);
    }

    public URL resolve(String path) throws MalformedURLException {
        if (path == null || path.isEmpty()) {
            throw new MalformedURLException("gateway path is empty");
        }
        String suffix = path.startsWith("/") ? path : "/" + path;
        return new URL(base + suffix);
    }

    public String requestPath(String path) throws MalformedURLException {
        return resolve(path).getFile();
    }

    public String host() {
        return baseUrl.getHost();
    }

    public String hostHeader() {
        return baseUrl.getPort() > 0 ? host() + ":" + port : host();
    }

    public int port() {
        return port;
    }

    public boolean isTls() {
        return tls;
    }
}

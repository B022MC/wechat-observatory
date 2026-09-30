package cc.wechat.observatory.gateway;

import org.junit.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.AbstractExecutorService;
import java.util.concurrent.TimeUnit;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

public class DiagnosticsReporterTest {
    /** Runs submitted work inline so tests are deterministic. */
    private static final class InlineExecutor extends AbstractExecutorService {
        @Override public void execute(Runnable command) { command.run(); }
        @Override public void shutdown() { }
        @Override public List<Runnable> shutdownNow() { return new ArrayList<>(); }
        @Override public boolean isShutdown() { return false; }
        @Override public boolean isTerminated() { return false; }
        @Override public boolean awaitTermination(long timeout, TimeUnit unit) { return true; }
    }

    private static final class Recording implements DiagnosticsReporter.Transport {
        final List<String> bodies = new ArrayList<>();
        final List<String> paths = new ArrayList<>();
        boolean fail;

        @Override
        public void post(String path, String body) throws Exception {
            paths.add(path);
            bodies.add(body);
            if (fail) {
                throw new IllegalStateException("network down");
            }
        }
    }

    private static DiagnosticsReporter.Report report(String stage, String message) {
        DiagnosticsReporter.Report report = new DiagnosticsReporter.Report();
        report.apiKey = "m-key";
        report.device = "61538M";
        report.stage = stage;
        report.message = message;
        report.moduleVersion = "0.1.9-diagnostics/10";
        return report;
    }

    @Test
    public void repeatedFailureIsSentOncePerWindow() {
        DiagnosticsReporter reporter = new DiagnosticsReporter(new InlineExecutor());
        Recording transport = new Recording();
        long now = 1_000_000L;
        assertTrue(reporter.report(transport, report("identity", "pending"), now));
        for (int i = 1; i <= 20; i++) {
            assertFalse(reporter.report(transport, report("identity", "pending"), now + i * 3_000L));
        }
        assertTrue(reporter.report(transport, report("identity", "pending"), now + DiagnosticsReporter.REPEAT_WINDOW_MS));
        assertEquals(2, transport.bodies.size());
        assertEquals(DiagnosticsReporter.PATH, transport.paths.get(0));
    }

    @Test
    public void differentFailuresRespectTheMinimumInterval() {
        DiagnosticsReporter reporter = new DiagnosticsReporter(new InlineExecutor());
        Recording transport = new Recording();
        long now = 5_000_000L;
        assertTrue(reporter.report(transport, report("startup", "capability"), now));
        assertFalse(reporter.report(transport, report("register", "invalid api key"), now + 1_000L));
        assertTrue(reporter.report(transport, report("register", "invalid api key"), now + DiagnosticsReporter.MIN_INTERVAL_MS));
        assertEquals(2, transport.bodies.size());
    }

    @Test
    public void transportFailureNeverReachesTheCaller() {
        DiagnosticsReporter reporter = new DiagnosticsReporter(new InlineExecutor());
        Recording transport = new Recording();
        transport.fail = true;
        assertTrue(reporter.report(transport, report("worker", "boom"), 0L));
        assertFalse(reporter.report(null, report("worker", "boom"), 60_000L));
        assertFalse(reporter.report(transport, null, 120_000L));
        DiagnosticsReporter.Report blank = report("", "x");
        assertFalse(reporter.report(transport, blank, 180_000L));
        assertEquals(1, transport.bodies.size());
    }

    @Test
    public void bodyIsEscapedJsonWithBoundedMessage() {
        StringBuilder huge = new StringBuilder();
        for (int i = 0; i < 2000; i++) {
            huge.append('x');
        }
        String body = DiagnosticsReporter.body(report("register", "bridge returned HTTP 400: {\"code\":\"a\"}\nnext" + huge));
        assertTrue(body.startsWith("{\"api_key\":\"m-key\",\"device\":\"61538M\",\"wxid\":\"\",\"stage\":\"register\","));
        assertTrue(body.contains("\\\"code\\\":\\\"a\\\"}\\nnext"));
        assertFalse(body.contains("\n"));
        int start = body.indexOf("\"message\":\"") + "\"message\":\"".length();
        int end = body.indexOf("\",\"module_version\"");
        String escaped = body.substring(start, end);
        String unescaped = escaped.replace("\\\"", "\"").replace("\\n", "\n");
        assertEquals(DiagnosticsReporter.MAX_MESSAGE_CHARS, unescaped.length());
        assertTrue(body.endsWith("\"module_version\":\"0.1.9-diagnostics/10\",\"wechat_version\":\"\",\"android\":\"\"}"));
    }
}

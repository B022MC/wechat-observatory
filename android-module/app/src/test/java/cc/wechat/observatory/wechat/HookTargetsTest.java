package cc.wechat.observatory.wechat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.Properties;

import org.junit.Test;

public final class HookTargetsTest {
    private static final String RUNTIME_BIND = "8.0.76/3141";

    private static Properties bound() {
        Properties properties = new Properties();
        properties.setProperty(HookTargets.KEY_BIND, RUNTIME_BIND);
        return properties;
    }

    @Test
    public void emptyConfigKeepsBuiltInTargets() {
        HookTargets targets = HookTargets.resolve(new Properties(), RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_OBSERVATION_CLASS, targets.observationClass);
        assertEquals(HookTargets.DEFAULT_OBSERVATION_METHOD, targets.observationMethod);
        assertEquals(HookTargets.DEFAULT_APP_CLASS, targets.appClass);
        assertFalse(targets.overridden);
        assertEquals("", targets.rejection);
    }

    @Test
    public void nullConfigKeepsBuiltInTargets() {
        HookTargets targets = HookTargets.resolve(null, RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_OBSERVATION_CLASS, targets.observationClass);
        assertFalse(targets.overridden);
    }

    @Test
    public void matchingBindAppliesOverride() {
        Properties properties = bound();
        properties.setProperty(HookTargets.KEY_OBSERVATION_CLASS, "com.tencent.wcdb.database.SQLiteDatabaseX");
        properties.setProperty(HookTargets.KEY_APP_CLASS, "com.tencent.mm.app.MMApplicationLikeX");

        HookTargets targets = HookTargets.resolve(properties, RUNTIME_BIND);

        assertEquals("com.tencent.wcdb.database.SQLiteDatabaseX", targets.observationClass);
        assertEquals("com.tencent.mm.app.MMApplicationLikeX", targets.appClass);
        // Untouched fields stay on the built-in default.
        assertEquals(HookTargets.DEFAULT_OBSERVATION_METHOD, targets.observationMethod);
        assertEquals(HookTargets.DEFAULT_APP_ATTACH_METHOD, targets.appAttachMethod);
        assertTrue(targets.overridden);
        assertEquals("", targets.rejection);
    }

    @Test
    public void overrideForAnotherWeChatBuildIsRefusedEntirely() {
        Properties properties = bound();
        properties.setProperty(HookTargets.KEY_OBSERVATION_CLASS, "com.tencent.wcdb.database.SQLiteDatabaseX");

        HookTargets targets = HookTargets.resolve(properties, "8.0.78/3180");

        assertEquals(HookTargets.DEFAULT_OBSERVATION_CLASS, targets.observationClass);
        assertFalse(targets.overridden);
        assertEquals("bind-mismatch", targets.rejection);
    }

    @Test
    public void overrideWithoutBindIsRefused() {
        Properties properties = new Properties();
        properties.setProperty(HookTargets.KEY_APP_CLASS, "com.tencent.mm.app.MMApplicationLikeX");

        HookTargets targets = HookTargets.resolve(properties, RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_APP_CLASS, targets.appClass);
        assertFalse(targets.overridden);
        assertEquals("missing-bind", targets.rejection);
    }

    @Test
    public void unknownRuntimeBindNeverSatisfiesAnOverride() {
        Properties properties = bound();

        HookTargets targets = HookTargets.resolve(properties, "unknown");

        assertFalse(targets.overridden);
        assertEquals("bind-mismatch", targets.rejection);
    }

    @Test
    public void malformedClassNameFallsBackWithoutDiscardingValidOnes() {
        Properties properties = bound();
        properties.setProperty(HookTargets.KEY_OBSERVATION_CLASS, "com.tencent.mm; rm -rf");
        properties.setProperty(HookTargets.KEY_APP_CLASS, "com.tencent.mm.app.MMApplicationLikeX");

        HookTargets targets = HookTargets.resolve(properties, RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_OBSERVATION_CLASS, targets.observationClass);
        assertEquals("com.tencent.mm.app.MMApplicationLikeX", targets.appClass);
        assertTrue(targets.overridden);
        assertTrue(targets.rejection.startsWith("invalid-class:"));
    }

    @Test
    public void malformedMethodNameFallsBackToDefault() {
        Properties properties = bound();
        properties.setProperty(HookTargets.KEY_OBSERVATION_METHOD, "insert With Space");

        HookTargets targets = HookTargets.resolve(properties, RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_OBSERVATION_METHOD, targets.observationMethod);
        assertFalse(targets.overridden);
        assertTrue(targets.rejection.startsWith("invalid-method:"));
    }

    @Test
    public void singleSegmentClassNameIsRejected() {
        Properties properties = bound();
        properties.setProperty(HookTargets.KEY_APP_CLASS, "MMApplicationLike");

        HookTargets targets = HookTargets.resolve(properties, RUNTIME_BIND);

        assertEquals(HookTargets.DEFAULT_APP_CLASS, targets.appClass);
        assertTrue(targets.rejection.startsWith("invalid-class:"));
    }

    @Test
    public void describeReportsSourceAndRejection() {
        HookTargets builtIn = HookTargets.resolve(new Properties(), RUNTIME_BIND);
        assertTrue(builtIn.describe().contains("source=builtin"));

        Properties mismatched = bound();
        HookTargets refused = HookTargets.resolve(mismatched, "8.0.78/3180");
        assertTrue(refused.describe().contains("rejected=bind-mismatch"));
    }
}

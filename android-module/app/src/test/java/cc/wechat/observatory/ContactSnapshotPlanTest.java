package cc.wechat.observatory;

import org.junit.Test;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;

public class ContactSnapshotPlanTest {
    @Test
    public void configuredLimitCapsFilteredOutput() {
        assertEquals(10000, ContactSnapshotPlan.outputLimit(0));
        assertEquals(750, ContactSnapshotPlan.outputLimit(750));
        assertEquals(10000, ContactSnapshotPlan.outputLimit(20000));
    }

    @Test
    public void queryDoesNotTruncateRawRowsBeforeFiltering() {
        assertFalse(ContactSnapshotPlan.CONTACT_QUERY.toUpperCase().contains("LIMIT"));
    }
}

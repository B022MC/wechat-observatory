package cc.wechat.observatory.wechat;

public final class SendResult {
    public final boolean ok;
    public final long chatRecordId;
    public final String error;

    private SendResult(boolean ok, long chatRecordId, String error) {
        this.ok = ok;
        this.chatRecordId = chatRecordId;
        this.error = error;
    }

    public static SendResult sent(long chatRecordId) {
        return new SendResult(true, chatRecordId, "");
    }

    public static SendResult failed(String error) {
        return failed(0L, error);
    }

    public static SendResult failed(long chatRecordId, String error) {
        return new SendResult(false, chatRecordId, error == null ? "send failed" : error);
    }
}

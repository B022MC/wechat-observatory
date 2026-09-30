package cc.wechat.observatory.model;

import cc.wechat.observatory.util.Strings;

public final class MessagePayload {
    public String id;
    public long eventId;
    public long chatRecordId;
    public String apiKey;
    public String ownerWxid;
    public String accountSession;
    public String device;
    public String chatId;
    public String chatKind;
    public String direction;
    public String from;
    public String to;
    public String roomId;
    public String sender;
    public String text;
    public int messageType;
    public String mediaKind;
    public String mediaMime;
    public String mediaName;
    public String mediaBase64;
    public int mediaSize;
    public long createTime;

    public String toJson() {
        return "{"
                + "\"id\":\"" + Strings.json(id) + "\","
                + "\"event_id\":" + eventId + ","
                + "\"chat_record_id\":" + chatRecordId + ","
                + "\"api_key\":\"" + Strings.json(apiKey) + "\","
                + "\"owner_wxid\":\"" + Strings.json(ownerWxid) + "\","
                + "\"account_session\":\"" + Strings.json(accountSession) + "\","
                + "\"device\":\"" + Strings.json(device) + "\","
                + "\"chat_id\":\"" + Strings.json(chatId) + "\","
                + "\"chat_kind\":\"" + Strings.json(chatKind) + "\","
                + "\"direction\":\"" + Strings.json(direction) + "\","
                + "\"from\":\"" + Strings.json(from) + "\","
                + "\"to\":\"" + Strings.json(to) + "\","
                + "\"room_id\":\"" + Strings.json(roomId) + "\","
                + "\"sender\":\"" + Strings.json(sender) + "\","
                + "\"text\":\"" + Strings.json(text) + "\","
                + "\"message_type\":" + messageType + ","
                + "\"media_kind\":\"" + Strings.json(mediaKind) + "\","
                + "\"media_mime\":\"" + Strings.json(mediaMime) + "\","
                + "\"media_name\":\"" + Strings.json(mediaName) + "\","
                + "\"media_size\":" + mediaSize + ","
                + "\"media_base64\":\"" + Strings.json(mediaBase64) + "\","
                + "\"create_time\":" + createTime
                + "}";
    }
}

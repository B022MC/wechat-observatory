package cc.wechat.observatory;

import android.app.Activity;
import android.content.Context;
import android.content.SharedPreferences;
import android.os.Bundle;
import android.text.InputType;
import android.text.method.PasswordTransformationMethod;
import android.view.Gravity;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import java.util.LinkedHashMap;
import java.util.Map;

public final class SettingsActivity extends Activity {
    private static final String DEFAULT_BRIDGE_URL = "http://192.168.1.10:8088";
    private static final String DEFAULT_POLL_INTERVAL_MS = "1000";
    private static final String DEFAULT_POLL_LIMIT = "1";
    private static final String DEFAULT_CONTACT_SYNC_INTERVAL_MS = "600000";
    private static final String DEFAULT_CONTACT_SYNC_LIMIT = "1000";
    private static final String DEFAULT_CONTACT_INCLUDE_CHATROOMS = "1";
    private static final String DEFAULT_MEDIA_UPLOAD_ENABLED = "0";
    private static final String DEFAULT_MEDIA_UPLOAD_LIMIT_BYTES = "5242880";
    private static final String[] SENSITIVE_CONFIG_KEYS = new String[]{"bridge_url", "api_key"};
    private static final Map<String, String> DEFAULTS = new LinkedHashMap<>();

    static {
        DEFAULTS.put("enabled", "1");
        DEFAULTS.put("bridge_url", DEFAULT_BRIDGE_URL);
        DEFAULTS.put("api_key", "");
        DEFAULTS.put("poll_interval_ms", DEFAULT_POLL_INTERVAL_MS);
        DEFAULTS.put("poll_limit", DEFAULT_POLL_LIMIT);
        DEFAULTS.put("contact_sync_interval_ms", DEFAULT_CONTACT_SYNC_INTERVAL_MS);
        DEFAULTS.put("contact_sync_limit", DEFAULT_CONTACT_SYNC_LIMIT);
        DEFAULTS.put("contact_include_chatrooms", DEFAULT_CONTACT_INCLUDE_CHATROOMS);
        DEFAULTS.put("media_upload_enabled", DEFAULT_MEDIA_UPLOAD_ENABLED);
        DEFAULTS.put("media_upload_limit_bytes", DEFAULT_MEDIA_UPLOAD_LIMIT_BYTES);
    }

    private final Map<String, EditText> fields = new LinkedHashMap<>();

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setTitle(getString(R.string.settings_title));
        setContentView(createContentView());
        loadValues();
    }

    private View createContentView() {
        ScrollView scrollView = new ScrollView(this);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(18), dp(18), dp(18), dp(24));
        scrollView.addView(root);

        TextView title = new TextView(this);
        title.setText(R.string.settings_title);
        title.setTextSize(22);
        title.setGravity(Gravity.START);
        title.setPadding(0, 0, 0, dp(6));
        root.addView(title, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT));

        TextView hint = new TextView(this);
        hint.setText(R.string.settings_hint);
        hint.setTextSize(14);
        hint.setPadding(0, 0, 0, dp(14));
        root.addView(hint);

        addField(root, "bridge_url", R.string.label_bridge_url, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        addField(root, "api_key", R.string.label_api_key, InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        maskSensitiveFields();
        addField(root, "poll_interval_ms", R.string.label_poll_interval, InputType.TYPE_CLASS_NUMBER);
        addField(root, "poll_limit", R.string.label_poll_limit, InputType.TYPE_CLASS_NUMBER);
        addField(root, "contact_sync_interval_ms", R.string.label_contact_sync_interval, InputType.TYPE_CLASS_NUMBER);
        addField(root, "contact_sync_limit", R.string.label_contact_sync_limit, InputType.TYPE_CLASS_NUMBER);
        addField(root, "contact_include_chatrooms", R.string.label_contact_include_chatrooms, InputType.TYPE_CLASS_NUMBER);
        addField(root, "media_upload_enabled", R.string.label_media_upload_enabled, InputType.TYPE_CLASS_NUMBER);
        addField(root, "media_upload_limit_bytes", R.string.label_media_upload_limit, InputType.TYPE_CLASS_NUMBER);

        Button saveButton = new Button(this);
        saveButton.setText(R.string.action_save);
        saveButton.setAllCaps(false);
        saveButton.setOnClickListener(v -> saveValues());
        LinearLayout.LayoutParams saveParams = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
        saveParams.topMargin = dp(12);
        root.addView(saveButton, saveParams);

        TextView footer = new TextView(this);
        footer.setText(R.string.settings_restart_hint);
        footer.setTextSize(13);
        footer.setPadding(0, dp(12), 0, 0);
        root.addView(footer);

        return scrollView;
    }

    private void addField(LinearLayout root, String key, int labelResId, int inputType) {
        TextView label = new TextView(this);
        label.setText(labelResId);
        label.setTextSize(14);
        label.setPadding(0, dp(10), 0, dp(4));
        root.addView(label);

        EditText editText = new EditText(this);
        editText.setSingleLine(true);
        editText.setInputType(inputType);
        editText.setTextSize(15);
        editText.setSelectAllOnFocus(false);
        root.addView(editText, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT));
        fields.put(key, editText);
    }

    private void maskSensitiveFields() {
        for (String key : SENSITIVE_CONFIG_KEYS) {
            EditText field = fields.get(key);
            if (field != null) {
                field.setTransformationMethod(PasswordTransformationMethod.getInstance());
            }
        }
    }

    private void loadValues() {
        SharedPreferences prefs = getSharedPreferences(BridgeConfigProvider.PREFS_NAME, Context.MODE_PRIVATE);
        for (String key : BridgeConfigProvider.CONFIG_KEYS) {
            if (fields.containsKey(key)) {
                setValue(key, prefs.getString(key, DEFAULTS.get(key)));
            }
        }
    }

    private void saveValues() {
        SharedPreferences prefs = getSharedPreferences(BridgeConfigProvider.PREFS_NAME, Context.MODE_PRIVATE);
        SharedPreferences.Editor editor = prefs.edit();
        for (String key : BridgeConfigProvider.CONFIG_KEYS) {
            if ("enabled".equals(key)) {
                editor.putString(key, "1");
                continue;
            }
            EditText field = fields.get(key);
            if (field != null) {
                String value = field.getText().toString().trim();
                if (value.isEmpty()) {
                    editor.remove(key);
                } else {
                    editor.putString(key, value);
                }
            }
        }
        editor.commit();
        BridgeConfigFiles.writeExternalMirror(this, prefs);
        maskSensitiveFields();
        Toast.makeText(this, R.string.settings_saved, Toast.LENGTH_LONG).show();
    }

    private void setValue(String key, String value) {
        EditText editText = fields.get(key);
        if (editText != null) {
            editText.setText(value == null ? "" : value);
        }
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density + 0.5f);
    }
}

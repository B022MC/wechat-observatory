import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  installationAccountText,
  installationDisplayName,
  installationModelText,
  installationShortId,
  installationStateLabel,
  installationStateTone,
  installationSwitchPending,
  installationVersionsText,
  parseApiError
} from '../src/installations.ts';

test('state labels distinguish current, current-but-offline, standby and offline phones', () => {
  assert.equal(installationStateLabel({ active: true, state: 'active' }), '在用');
  assert.equal(installationStateLabel({ active: true, state: 'offline' }), '在用·离线');
  assert.equal(installationStateLabel({ active: false, state: 'standby' }), '待命');
  assert.equal(installationStateLabel({ active: false, state: 'offline' }), '离线');
  assert.equal(installationStateTone({ active: true, state: 'active' }), 'success');
  assert.equal(installationStateTone({ active: true, state: 'offline' }), 'destructive');
  assert.equal(installationStateTone({ active: false, state: 'offline' }), 'secondary');
});

test('old module versions without a device model fall back to 旧版模块 + short id', () => {
  const legacy = { id: 7, short_id: '1a2b3c4d', device_model: '' };
  assert.equal(installationModelText(legacy), '旧版模块');
  assert.equal(installationShortId(legacy), '#1a2b3c4d');
  assert.equal(installationDisplayName(legacy), '旧版模块 #1a2b3c4d');
  assert.equal(installationDisplayName({ id: 8, short_id: '', device_model: 'Xiaomi 2312DRAABC' }), 'Xiaomi 2312DRAABC #8');
});

test('versions and account only include reported values', () => {
  assert.equal(installationVersionsText({ android_version: '14', wechat_version: '8.0.50', module_version: '0.1.12' }), 'Android 14 · 微信 8.0.50 · 模块 0.1.12');
  assert.equal(installationVersionsText({ wechat_version: '8.0.50' }), '微信 8.0.50');
  assert.equal(installationVersionsText({}), '');
  assert.equal(installationAccountText({ wechat_nickname: '小明', owner_wxid: 'wxid_a' }), '小明（wxid_a）');
  assert.equal(installationAccountText({ owner_wxid: 'wxid_a' }), 'wxid_a');
  assert.equal(installationAccountText({}), '');
});

test('switch pending follows the row flag or the pending request target', () => {
  assert.equal(installationSwitchPending({ id: 2, switch_pending: true }), true);
  assert.equal(installationSwitchPending({ id: 2, switch_pending: false }, { installation_id: 2 }), true);
  assert.equal(installationSwitchPending({ id: 3, switch_pending: false }, { installation_id: 2 }), false);
});

test('server error bodies expose code and message; plain text passes through', () => {
  const conflict = new Error(JSON.stringify({ ok: false, code: 'installation_active', message: 'already active' }));
  assert.deepEqual(parseApiError(conflict, 'fallback'), { code: 'installation_active', message: 'already active' });
  assert.deepEqual(parseApiError(new Error(JSON.stringify({ ok: false, code: 'switch_failed' })), 'fallback'), { code: 'switch_failed', message: 'switch_failed' });
  assert.deepEqual(parseApiError(new Error('HTTP 502'), 'fallback'), { code: '', message: 'HTTP 502' });
  assert.deepEqual(parseApiError(new Error(''), 'fallback'), { code: '', message: 'fallback' });
  assert.deepEqual(parseApiError(undefined, 'fallback'), { code: '', message: 'fallback' });
});

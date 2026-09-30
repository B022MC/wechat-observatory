import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AccountScope, belongsToAccount } from '../src/accountScope.ts';

test('late A response stays invalid after A -> B -> A, even on legacy servers', () => {
  const scope = new AccountScope();
  scope.update('phone', 'A', 0);
  const old = scope.begin('contacts', 'phone', 'A');
  scope.update('phone', 'B', 0);
  scope.update('phone', 'A', 0);
  assert.equal(scope.matches(old), false);
  assert.equal(scope.matches(scope.begin('contacts', 'phone', 'A')), true);
});

test('server generation detects a complete round trip between status polls', () => {
  const scope = new AccountScope();
  scope.update('phone', 'A', 1);
  const old = scope.begin('send', 'phone', 'A');
  scope.update('phone', 'A', 3);
  assert.equal(scope.matches(old), false);
});

test('out of order searches and chat responses cannot replace newer results', () => {
  const scope = new AccountScope();
  scope.update('phone', 'A', 1);
  const first = scope.begin('contacts', 'phone', 'A');
  const second = scope.begin('contacts', 'phone', 'A');
  assert.equal(scope.matches(first), false);
  assert.equal(scope.matches(second), true);
  scope.selectChat('shared-friend');
  const message = scope.begin('messages', 'phone', 'A', 'shared-friend');
  scope.selectChat('other');
  scope.selectChat('shared-friend');
  assert.equal(scope.matches(message), false);
  assert.equal(scope.begin('messages', 'phone', 'A', 'other'), undefined);
});

test('shared peers only resolve against the selected owner and device', () => {
  assert.equal(belongsToAccount({ device: 'phone', owner_wxid: 'B', wxid: 'shared' }, 'phone', 'A'), false);
  assert.equal(belongsToAccount({ device: 'other', owner_wxid: 'A' }, 'phone', 'A'), false);
  assert.equal(belongsToAccount({ device: 'phone', owner_wxid: 'A' }, 'phone', 'A'), true);
  assert.equal(belongsToAccount({ device: 'phone' }, 'phone', ''), false);
});

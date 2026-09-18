import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

const readMigration = (name) => readFile(new URL(`../migrations/${name}`, import.meta.url), 'utf8');

test('registers M2.3 group migrations after the conversation schema', () => {
  const start = orderedMigrationNames.indexOf('014_create_groups.sql');
  assert.deepEqual(orderedMigrationNames.slice(start, start + 2), [
    '014_create_groups.sql',
    '015_create_group_members.sql',
  ]);
  assert.ok(orderedMigrationNames.indexOf('014_create_groups.sql') > orderedMigrationNames.indexOf('013_create_messages.sql'));
});

test('M2.3 migrations define version, role, lifecycle and query constraints without foreign keys', async () => {
  const [groups, members] = await Promise.all([
    readMigration('014_create_groups.sql'),
    readMigration('015_create_group_members.sql'),
  ]);
  assert.match(groups, /version\s+BIGINT UNSIGNED NOT NULL DEFAULT 1/);
  assert.match(groups, /KEY idx_groups_owner_id \(owner_id, id\)/);
  assert.match(groups, /CHAR_LENGTH\(name\) BETWEEN 1 AND 80/);
  assert.match(members, /UNIQUE KEY uk_group_members_group_user \(group_id, user_id\)/);
  assert.match(members, /KEY idx_group_members_user_status_group \(user_id, status, group_id\)/);
  assert.match(members, /CHECK \(role IN \('member', 'admin', 'owner'\)\)/);
  assert.match(members, /CHECK \(status IN \('active', 'left'\)\)/);
  assert.doesNotMatch(groups + members, /FOREIGN KEY/i);
});

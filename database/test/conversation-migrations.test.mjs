import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

const readMigration = (name) => readFile(new URL(`../migrations/${name}`, import.meta.url), 'utf8');

test('registers M2.2 conversation migrations in dependency order', () => {
  assert.deepEqual(orderedMigrationNames.slice(-3), [
    '011_create_conversations.sql',
    '012_create_conversation_members.sql',
    '013_create_messages.sql',
  ]);
});

test('M2.2 migrations define stable pair, member, sequence and idempotency constraints', async () => {
  const [conversations, members, messages] = await Promise.all([
    readMigration('011_create_conversations.sql'),
    readMigration('012_create_conversation_members.sql'),
    readMigration('013_create_messages.sql'),
  ]);
  assert.match(conversations, /UNIQUE KEY uk_conversations_direct_pair \(direct_low_id, direct_high_id\)/);
  assert.match(conversations, /KEY idx_conversations_activity \(last_message_at, id\)/);
  assert.match(members, /UNIQUE KEY uk_conversation_members_conversation_user \(conversation_id, user_id\)/);
  assert.match(members, /last_read_seq\s+BIGINT UNSIGNED NOT NULL DEFAULT 0/);
  assert.match(messages, /UNIQUE KEY uk_messages_conversation_seq \(conversation_id, seq\)/);
  assert.match(messages, /UNIQUE KEY uk_messages_client_id \(conversation_id, sender_id, client_message_id\)/);
  assert.match(messages, /client_message_id\s+CHAR\(36\) CHARACTER SET ascii COLLATE ascii_bin NOT NULL/);
  assert.match(messages, /metadata\s+JSON NOT NULL DEFAULT \(JSON_OBJECT\(\)\)/);
});

import assert from 'node:assert/strict';
import test from 'node:test';
import { ensureFriendships, ensureScriptChunkCount, removeLegacyForeignKeys } from '../lib/special-migrations.mjs';

test('adds chunk_count only when it is missing', async () => {
  const queries = [];
  const connection = {
    async execute() { return [[]]; },
    async query(sql) { queries.push(sql); return [[]]; },
  };
  await ensureScriptChunkCount(connection, 'ALTER TABLE scripts ADD COLUMN chunk_count INT UNSIGNED NOT NULL DEFAULT 0');
  assert.equal(queries.length, 1);
});

test('rejects incompatible chunk_count column', async () => {
  const connection = {
    async execute() { return [[{ DATA_TYPE: 'varchar', COLUMN_TYPE: 'varchar(10)', IS_NULLABLE: 'YES', COLUMN_DEFAULT: null }]]; },
    async query() { throw new Error('should not execute DDL'); },
  };
  await assert.rejects(ensureScriptChunkCount(connection, 'ALTER TABLE scripts'), /incompatible/);
});

test('only drops legacy objects returned by information_schema', async () => {
  const queries = [];
  const connection = {
    async execute(_sql, [table, name]) { return [[{ count: table === 'scripts' && name === 'fk_scripts_user' ? 1 : 0 }]]; },
    async query(sql) { queries.push(sql); return [[]]; },
  };
  await removeLegacyForeignKeys(connection);
  assert.deepEqual(queries, ['ALTER TABLE `scripts` DROP FOREIGN KEY `fk_scripts_user`']);
});

test('creates friendships and nickname search index when missing', async () => {
  const queries = [];
  const connection = {
    async execute(sql) {
      if (sql.includes('information_schema.TABLES')) return [[{ count: 0 }]];
      return [[]];
    },
    async query(sql) { queries.push(sql); return [[]]; },
  };
  await ensureFriendships(connection, 'CREATE TABLE friendships (id BIGINT PRIMARY KEY)');
  assert.deepEqual(queries, [
    'CREATE TABLE friendships (id BIGINT PRIMARY KEY)',
    'ALTER TABLE users ADD INDEX idx_users_nickname_id_deleted (nickname, id, deleted_at)',
  ]);
});

test('accepts the exact existing nickname search index', async () => {
  const connection = {
    async execute(sql) {
      if (sql.includes('information_schema.TABLES')) return [[{ count: 1 }]];
      return [[
        { NON_UNIQUE: 1, SEQ_IN_INDEX: 1, COLUMN_NAME: 'nickname' },
        { NON_UNIQUE: 1, SEQ_IN_INDEX: 2, COLUMN_NAME: 'id' },
        { NON_UNIQUE: 1, SEQ_IN_INDEX: 3, COLUMN_NAME: 'deleted_at' },
      ]];
    },
    async query() { throw new Error('should not execute DDL'); },
  };
  await ensureFriendships(connection, 'CREATE TABLE friendships');
});

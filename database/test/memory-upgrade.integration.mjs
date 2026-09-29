// Explicitly invoked against a disposable database, never included in db:test.
import assert from 'node:assert/strict';
import mysql from 'mysql2/promise';
import { readFile } from 'node:fs/promises';
import { migrationConfig } from '../lib/config.mjs';
import { applyMigrations } from '../lib/runner.mjs';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

const { lockTimeoutSeconds, ...config } = migrationConfig();
if (!/^[a-zA-Z0-9_]+_memory_test$/.test(config.database)) throw new Error('requires a disposable *_memory_test database');
const db = await mysql.createConnection({ ...config, multipleStatements: false });
try {
  const [tables] = await db.query('SHOW TABLES');
  assert.equal(tables.length, 0, 'upgrade fixture requires an empty disposable database');
  await applyMigrations(db, { migrationNames: orderedMigrationNames.slice(0, 17), lockTimeoutSeconds });
  const fixtures = await Promise.all(['v1', 'v2'].map(async (version) => JSON.parse(await readFile(new URL(`../../go-backend/internal/service/testdata/phase3/legacy-${version}.json`, import.meta.url), 'utf8'))));
  const [v1, v2] = fixtures.map((fixture) => JSON.stringify(fixture.redis_snapshot));
  await db.execute('INSERT INTO game_saves (room_id,save_name,round_number,redis_snapshot,recent_messages,is_auto) VALUES (71,?,10,?,JSON_ARRAY(),TRUE),(71,?,5,?,JSON_ARRAY(),FALSE),(71,?,5,?,JSON_ARRAY(),FALSE)', ['legacy auto', v1, 'legacy manual 1', v2, 'legacy manual 2', v2]);
  const [before] = await db.query('SELECT id, CAST(redis_snapshot AS CHAR) AS snapshot FROM game_saves ORDER BY id');
  await applyMigrations(db, { lockTimeoutSeconds });
  const [after] = await db.query('SELECT id, CAST(redis_snapshot AS CHAR) AS snapshot FROM game_saves ORDER BY id');
  assert.deepEqual(after, before, 'upgrade must preserve every legacy save and JSON');
  const [scopes] = await db.query('SELECT auto_timeline_scope,timeline_id,memory_position FROM game_saves ORDER BY id');
  assert.deepEqual(scopes.map((row) => row.auto_timeline_scope), ['legacy', null, null]);
  assert.ok(scopes.every((row) => row.timeline_id === null && row.memory_position === null));
  await assert.rejects(db.execute('INSERT INTO game_saves (room_id,round_number,redis_snapshot,recent_messages,is_auto) VALUES (71,10,?,JSON_ARRAY(),TRUE)', [v1]), { code: 'ER_DUP_ENTRY' });
  for (const timeline of ['10000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000002']) {
    await db.execute('INSERT INTO game_saves (room_id,round_number,redis_snapshot,recent_messages,is_auto,timeline_id,memory_position) VALUES (71,10,?,JSON_ARRAY(),TRUE,?,20)', [v1, timeline]);
  }
  await assert.rejects(db.execute('INSERT INTO game_saves (room_id,round_number,redis_snapshot,recent_messages,is_auto,timeline_id,memory_position) VALUES (71,10,?,JSON_ARRAY(),TRUE,?,20)', [v1, '10000000-0000-4000-8000-000000000001']), { code: 'ER_DUP_ENTRY' });
  await assert.rejects(db.execute('INSERT INTO game_saves (room_id,redis_snapshot,recent_messages,timeline_id) VALUES (71,?,JSON_ARRAY(),?)', [v1, '10000000-0000-4000-8000-000000000003']), { code: 'ER_CHECK_CONSTRAINT_VIOLATED' });
  const [indexes] = await db.query('SHOW INDEX FROM game_saves');
  assert.ok(!indexes.some((row) => row.Key_name === 'idx_game_saves_auto_round'));
  assert.ok(indexes.some((row) => row.Key_name === 'idx_game_saves_auto_timeline_round'));
  await applyMigrations(db, { lockTimeoutSeconds });
  console.log('MySQL 8.4: 017→020 upgrade, legacy JSON preservation, branch autosave uniqueness and rerun passed');
} finally { await db.end(); }

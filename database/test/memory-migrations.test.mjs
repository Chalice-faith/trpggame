import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { orderedMigrationNames } from '../migrations/manifest.mjs';
import { applyMigrations, migrationChecksum } from '../lib/runner.mjs';

const readMigration = (name) => readFile(new URL(`../migrations/${name}`, import.meta.url));

test('memory migrations follow 017 and preserve legacy save scope', async () => {
  assert.deepEqual(orderedMigrationNames.slice(17), ['018_create_game_memory_timelines.sql', '019_create_game_action_records.sql', '020_extend_game_saves_memory.sql']);
  const bodies = await Promise.all(orderedMigrationNames.slice(17).map(async (name) => (await readMigration(name)).toString()));
  assert.match(bodies[1], /UNIQUE KEY uk_game_records_position \(room_id, timeline_id, position\)/);
  assert.match(bodies[1], /UNIQUE KEY uk_game_records_request \(room_id, request_namespace, request_id\)/);
  assert.match(bodies[2], /COALESCE\(timeline_id, 'legacy'\)/);
  assert.match(bodies[2], /DROP INDEX idx_game_saves_auto_round/);
  assert.doesNotMatch(bodies.join('\n'), /FOREIGN KEY|DELETE FROM|DROP TABLE/i);
});

test('017 upgrade executes new CREATE statements separately without replaying applied SQL', async () => {
  const applied = await Promise.all(orderedMigrationNames.slice(0, 17).map(async (version) => ({ version, checksum: migrationChecksum(await readMigration(version)) })));
  const queries = [], recorded = [];
  const connection = {
    async execute(sql, values) {
      if (sql.startsWith('SELECT GET_LOCK')) return [[{ locked: 1 }]];
      if (sql.startsWith('INSERT INTO schema_migrations')) recorded.push(values[0]);
      return [[]];
    },
    async query(sql) {
      if (sql.startsWith('SELECT version')) return [applied];
      queries.push(sql);
      return [[]];
    },
  };
  await applyMigrations(connection);
  assert.deepEqual(recorded, orderedMigrationNames.slice(17));
  assert.equal(queries.filter((sql) => /CREATE TABLE IF NOT EXISTS game_/.test(sql)).length, 4);
  assert.equal(queries.filter((sql) => /ALTER TABLE game_saves/.test(sql)).length, 1);
  assert.ok(queries.every((sql) => (sql.match(/CREATE TABLE/g) ?? []).length <= 1));
});

test('migration fixtures cannot reorder the manifest', async () => {
  await assert.rejects(applyMigrations({}, { migrationNames: [orderedMigrationNames[17]] }), /manifest prefix/);
});

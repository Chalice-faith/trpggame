import assert from 'node:assert/strict';
import test from 'node:test';
import { applyMigrations, migrationChecksum } from '../lib/runner.mjs';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

function connection({ applied = [], locked = 1 } = {}) {
  const calls = [];
  return {
    calls,
    async execute(sql, values = []) {
      calls.push(['execute', sql, values]);
      if (sql.startsWith('SELECT GET_LOCK')) return [[{ locked }]];
      if (sql.startsWith('SELECT RELEASE_LOCK')) return [[{ released: 1 }]];
      if (sql.includes('information_schema.TABLE_CONSTRAINTS')) return [[{ count: 0 }]];
      if (sql.includes('information_schema.STATISTICS')) {
        if (values.length === 2) return [[{ count: 0 }]];
        return [[]];
      }
      if (sql.includes('information_schema.COLUMNS')) return [[]];
      return [[]];
    },
    async query(sql) {
      calls.push(['query', sql]);
      if (sql.startsWith('SELECT version')) return [applied];
      return [[]];
    },
  };
}

const migrations = new Map(orderedMigrationNames.map((name) => [name, Buffer.from(`SELECT '${name}'`)]));

test('applies every pending migration and records raw-byte checksums', async () => {
  const db = connection();
  await applyMigrations(db, { readMigration: (name) => migrations.get(name) });
  const records = db.calls.filter(([kind, sql]) => kind === 'execute' && sql.startsWith('INSERT INTO schema_migrations'));
  assert.equal(records.length, orderedMigrationNames.length);
  assert.equal(records[0][2][1], migrationChecksum(migrations.get(orderedMigrationNames[0])));
  assert.ok(db.calls.some(([kind, sql]) => kind === 'execute' && sql.startsWith('SELECT RELEASE_LOCK')));
});

test('rejects unavailable migration lock without executing migrations', async () => {
  const db = connection({ locked: 0 });
  await assert.rejects(applyMigrations(db, { readMigration: (name) => migrations.get(name) }), /lock is unavailable/);
  assert.equal(db.calls.filter(([kind, sql]) => kind === 'query' && sql.startsWith('SELECT version')).length, 0);
});

test('rejects altered migration content after it was applied', async () => {
  const name = orderedMigrationNames[0];
  const db = connection({ applied: [{ version: name, checksum: 'not-a-checksum' }] });
  await assert.rejects(applyMigrations(db, { readMigration: (migrationName) => migrations.get(migrationName) }), /checksum mismatch/);
  assert.ok(db.calls.some(([kind, sql]) => kind === 'execute' && sql.startsWith('SELECT RELEASE_LOCK')));
});

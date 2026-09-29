import assert from 'node:assert/strict';
import mysql from 'mysql2/promise';
import { migrationConfig } from '../lib/config.mjs';
import { applyMigrations } from '../lib/runner.mjs';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

const { lockTimeoutSeconds, database, ...config } = migrationConfig();
if (!/^[a-zA-Z0-9_]+_memory_test$/.test(database)) throw new Error('requires a disposable *_memory_test database');
const db = await mysql.createConnection({ ...config, multipleStatements: false });
try {
  await db.query(`CREATE DATABASE IF NOT EXISTS \`${database}\``);
  await db.query(`USE \`${database}\``);
  const [tables] = await db.query('SHOW TABLES');
  assert.equal(tables.length, 0, 'empty fixture requires an empty disposable database');
  await applyMigrations(db, { lockTimeoutSeconds });
  await applyMigrations(db, { lockTimeoutSeconds });
  const [migrations] = await db.query('SELECT version FROM schema_migrations ORDER BY version');
  assert.deepEqual(migrations.map((row) => row.version), orderedMigrationNames);
  for (const table of ['game_memory_states', 'game_timelines', 'game_action_records', 'game_memory_operations']) {
    const [rows] = await db.query(`SELECT COUNT(*) AS n FROM ${table}`);
    assert.equal(rows[0].n, 0);
  }
  console.log('MySQL 8.4: empty 001→020 schema and idempotent rerun passed');
} finally { await db.end(); }

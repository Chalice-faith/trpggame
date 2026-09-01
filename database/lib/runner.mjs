import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { historicalMigrationChecksums, orderedMigrationNames } from '../migrations/manifest.mjs';
import { ensureAutoSaveUniqueness, ensureScriptChunkCount, removeLegacyForeignKeys } from './special-migrations.mjs';

const migrationDirectory = join(dirname(fileURLToPath(import.meta.url)), '..', 'migrations');
const migrationLockName = 'trpggame:migrations';
const createMigrationTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version VARCHAR(128) NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`;

export function migrationChecksum(body) {
  return createHash('sha256').update(body).digest('hex');
}

export async function applyMigrations(connection, { lockTimeoutSeconds = 10, readMigration = (name) => readFile(join(migrationDirectory, name)), log = () => {} } = {}) {
  const [lockRows] = await connection.execute('SELECT GET_LOCK(?, ?) AS locked', [migrationLockName, lockTimeoutSeconds]);
  if (lockRows[0]?.locked !== 1) throw new Error('database migration lock is unavailable');
  try {
    await connection.query(createMigrationTableSQL);
    const [records] = await connection.query('SELECT version, checksum FROM schema_migrations ORDER BY version');
    const applied = new Map(records.map(({ version, checksum }) => [version, checksum]));
    for (const name of orderedMigrationNames) {
      const body = await readMigration(name);
      const checksum = migrationChecksum(body);
      const recordedChecksum = applied.get(name);
      if (recordedChecksum !== undefined) {
        const acceptedHistorical = historicalMigrationChecksums.get(name)?.has(recordedChecksum.toLowerCase());
        if (recordedChecksum.toLowerCase() !== checksum && !acceptedHistorical) throw new Error(`checksum mismatch for ${name}`);
        continue;
      }
      await applyMigration(connection, name, body.toString());
      await connection.execute('INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)', [name, checksum]);
      log(`Applied ${name}`);
    }
  } finally {
    await connection.execute('SELECT RELEASE_LOCK(?)', [migrationLockName]);
  }
}

async function applyMigration(connection, name, body) {
  switch (name) {
    case '004_add_script_chunk_count.sql': return ensureScriptChunkCount(connection, body);
    case '008_add_auto_save_uniqueness.sql': return ensureAutoSaveUniqueness(connection);
    case '009_remove_foreign_keys.sql': return removeLegacyForeignKeys(connection);
    default: return connection.query(body);
  }
}

import assert from 'node:assert/strict';
import test from 'node:test';
import { migrationConfig } from '../lib/config.mjs';

test('migration config prioritizes migration-specific environment variables', () => {
  const config = migrationConfig({
    TRPG_MIGRATIONS_HOST: 'database',
    TRPG_MIGRATIONS_PORT: '3307',
    TRPG_MIGRATIONS_DATABASE: 'game',
    TRPG_MIGRATIONS_USER: 'migrator',
    TRPG_MIGRATIONS_PASSWORD: 'secret',
    TRPG_MIGRATIONS_LOCK_TIMEOUT_SECONDS: '20',
  });
  assert.deepEqual(config, {
    host: 'database', port: 3307, database: 'game', user: 'migrator', password: 'secret', lockTimeoutSeconds: 20,
  });
});

test('migration config uses application database variables as fallback', () => {
  const config = migrationConfig({ TRPG_DATABASE_PORT: '3308', TRPG_DATABASE_USER: 'app' });
  assert.equal(config.port, 3308);
  assert.equal(config.user, 'app');
  assert.equal(config.database, 'trpggame');
});

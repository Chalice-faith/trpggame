import mysql from 'mysql2/promise';
import { migrationConfig } from '../lib/config.mjs';
import { applyMigrations } from '../lib/runner.mjs';

const { lockTimeoutSeconds, ...connectionConfig } = migrationConfig();
const pool = mysql.createPool({
  ...connectionConfig,
  waitForConnections: true,
  connectionLimit: 1,
  multipleStatements: false,
});

try {
  const connection = await pool.getConnection();
  try {
    await applyMigrations(connection, { lockTimeoutSeconds, log: (message) => console.log(message) });
    console.log('Database migrations are up to date');
  } finally {
    connection.release();
  }
} catch (error) {
  console.error(`Database migration failed: ${error.message}`);
  process.exitCode = 1;
} finally {
  await pool.end();
}

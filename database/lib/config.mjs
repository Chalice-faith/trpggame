const defaults = {
  host: '127.0.0.1',
  port: 3306,
  database: 'trpggame',
  user: 'trpg',
  password: 'trpg123',
  lockTimeoutSeconds: 10,
};

function integerEnvironment(environment, name, fallback) {
  const value = environment[name];
  if (value === undefined || value === '') return fallback;
  const parsed = Number.parseInt(value, 10);
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new Error(`${name} must be a non-negative integer`);
  }
  return parsed;
}

export function migrationConfig(environment = process.env) {
  return {
    host: environment.TRPG_MIGRATIONS_HOST ?? environment.TRPG_DATABASE_HOST ?? defaults.host,
    port: integerEnvironment(environment, 'TRPG_MIGRATIONS_PORT', integerEnvironment(environment, 'TRPG_DATABASE_PORT', defaults.port)),
    database: environment.TRPG_MIGRATIONS_DATABASE ?? environment.TRPG_DATABASE_DBNAME ?? defaults.database,
    user: environment.TRPG_MIGRATIONS_USER ?? environment.TRPG_DATABASE_USER ?? defaults.user,
    password: environment.TRPG_MIGRATIONS_PASSWORD ?? environment.TRPG_DATABASE_PASSWORD ?? defaults.password,
    lockTimeoutSeconds: integerEnvironment(environment, 'TRPG_MIGRATIONS_LOCK_TIMEOUT_SECONDS', defaults.lockTimeoutSeconds),
  };
}

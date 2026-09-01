const scriptChunkColumnQuery = `
SELECT DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'scripts'
  AND COLUMN_NAME = 'chunk_count'`;

const autoSaveColumnQuery = `
SELECT EXTRA, GENERATION_EXPRESSION
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'game_saves'
  AND COLUMN_NAME = 'auto_round_number'`;

const autoSaveIndexQuery = `
SELECT NON_UNIQUE, SEQ_IN_INDEX, COLUMN_NAME
FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'game_saves'
  AND INDEX_NAME = 'idx_game_saves_auto_round'
ORDER BY SEQ_IN_INDEX`;

const addAutoSaveColumnSQL = `ALTER TABLE game_saves
ADD COLUMN auto_round_number INT UNSIGNED
GENERATED ALWAYS AS (CASE WHEN is_auto THEN round_number ELSE NULL END) STORED`;
const addAutoSaveIndexSQL = 'ALTER TABLE game_saves ADD UNIQUE KEY idx_game_saves_auto_round (room_id, auto_round_number)';

const legacyForeignKeys = [
  ['scripts', 'fk_scripts_user'], ['script_characters', 'fk_script_characters_script'],
  ['game_rooms', 'fk_game_rooms_script'], ['game_rooms', 'fk_game_rooms_owner'],
  ['room_players', 'fk_room_players_room'], ['room_players', 'fk_room_players_user'],
  ['room_players', 'fk_room_players_character'], ['game_saves', 'fk_game_saves_room'],
];
const legacyRelationshipIndexes = [
  ['script_characters', 'idx_script_characters_script'], ['game_rooms', 'idx_game_rooms_owner_created'],
  ['game_rooms', 'idx_game_rooms_script'], ['room_players', 'idx_room_players_user'],
  ['room_players', 'idx_room_players_character'],
];

export async function ensureScriptChunkCount(connection, migrationSQL) {
  const [rows] = await connection.execute(scriptChunkColumnQuery);
  if (rows.length === 0) {
    await connection.query(migrationSQL);
    return;
  }
  const [{ DATA_TYPE: dataType, COLUMN_TYPE: columnType, IS_NULLABLE: nullable, COLUMN_DEFAULT: defaultValue }] = rows;
  if (dataType.toLowerCase() !== 'int' || columnType.toLowerCase() !== 'int unsigned' || nullable.toUpperCase() !== 'NO' || String(defaultValue) !== '0') {
    throw new Error('scripts.chunk_count has an incompatible definition');
  }
}

export async function ensureAutoSaveUniqueness(connection) {
  const [columnRows] = await connection.execute(autoSaveColumnQuery);
  let columnExists = columnRows.length > 0;
  if (columnExists) {
    const [{ EXTRA: extra, GENERATION_EXPRESSION: expression }] = columnRows;
    const normalizedExtra = extra.trim().toUpperCase();
    const normalizedExpression = (expression ?? '').trim().toLowerCase();
    if (!normalizedExtra.includes('STORED GENERATED') || !normalizedExpression.includes('is_auto') || !normalizedExpression.includes('round_number')) {
      throw new Error('generated auto_round_number column has an incompatible definition');
    }
  }

  const [indexRows] = await connection.execute(autoSaveIndexQuery);
  const indexExists = indexRows.length > 0;
  if (indexExists && (indexRows.length !== 2 || indexRows[0].NON_UNIQUE !== 0 || indexRows[0].SEQ_IN_INDEX !== 1 || indexRows[0].COLUMN_NAME !== 'room_id' || indexRows[1].NON_UNIQUE !== 0 || indexRows[1].SEQ_IN_INDEX !== 2 || indexRows[1].COLUMN_NAME !== 'auto_round_number')) {
    throw new Error('idx_game_saves_auto_round has an incompatible definition');
  }
  if (indexExists && !columnExists) throw new Error('unique index exists without generated column');
  if (!columnExists) {
    await connection.query(addAutoSaveColumnSQL);
    columnExists = true;
  }
  if (!indexExists) await connection.query(addAutoSaveIndexSQL);
}

async function schemaObjectExists(connection, query, table, name) {
  const [rows] = await connection.execute(query, [table, name]);
  return rows[0].count > 0;
}

export async function removeLegacyForeignKeys(connection) {
  const foreignKeyQuery = `SELECT COUNT(*) AS count FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ? AND CONSTRAINT_TYPE = 'FOREIGN KEY'`;
  const indexQuery = `SELECT COUNT(*) AS count FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`;
  for (const [table, name] of legacyForeignKeys) {
    if (await schemaObjectExists(connection, foreignKeyQuery, table, name)) await connection.query(`ALTER TABLE \`${table}\` DROP FOREIGN KEY \`${name}\``);
  }
  for (const [table, name] of legacyRelationshipIndexes) {
    if (await schemaObjectExists(connection, indexQuery, table, name)) await connection.query(`ALTER TABLE \`${table}\` DROP INDEX \`${name}\``);
  }
}

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { orderedMigrationNames } from '../migrations/manifest.mjs';

const readMigration = (name) => readFile(new URL(`../migrations/${name}`, import.meta.url), 'utf8');

test('registers M2.4 room migrations after group migrations', () => {
  assert.deepEqual(orderedMigrationNames.slice(-2), [
    '016_extend_game_rooms.sql',
    '017_extend_room_players.sql',
  ]);
});

test('room extensions preserve solo rows and enforce multiplayer uniqueness', async () => {
  const [rooms, players] = await Promise.all([
    readMigration('016_extend_game_rooms.sql'),
    readMigration('017_extend_room_players.sql'),
  ]);
  assert.match(rooms, /room_code CHAR\(8\).*NULL/);
  assert.match(rooms, /UNIQUE KEY uk_game_rooms_room_code \(room_code\)/);
  assert.match(rooms, /version BIGINT UNSIGNED NOT NULL DEFAULT 1/);
  assert.match(rooms, /is_solo = TRUE OR room_code IS NOT NULL/);
  assert.match(players, /status VARCHAR\(16\) NOT NULL DEFAULT 'active'/);
  assert.match(players, /CASE WHEN status = 'active' THEN character_id ELSE NULL END/);
  assert.match(players, /UNIQUE KEY uk_room_players_active_character \(room_id, active_character_id\)/);
  assert.doesNotMatch(rooms + players, /FOREIGN KEY/i);
});

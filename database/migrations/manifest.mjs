export const orderedMigrationNames = [
  '001_create_users.sql',
  '002_create_scripts.sql',
  '003_create_script_characters.sql',
  '004_add_script_chunk_count.sql',
  '005_create_game_rooms.sql',
  '006_create_room_players.sql',
  '007_create_game_saves.sql',
  '008_add_auto_save_uniqueness.sql',
  '009_remove_foreign_keys.sql',
];

export const historicalMigrationChecksums = new Map([
  ['002_create_scripts.sql', new Set([
    'd8c4359b82e1210cf3fe8df7d7c531ff2a1251f3dbfcade99eb102e41d8bebea',
    '2fe51a840f449d8861021eaff6ac8e904f254931baa2b90636460d60e7d5a2d5',
  ])],
  ['003_create_script_characters.sql', new Set([
    '43eda40d0432f45ae01e0750f7b3cda00e28a397e4c3228d13c2b89c450ce10f',
    '185af08bd3c2865bb8eae9dd15e71afb7ebfeec135511b4a7082c9b38741c37d',
  ])],
  ['005_create_game_rooms.sql', new Set([
    '12056c0e8daa915bc2f6f340c207b94c6cb842fbf5e11595b665f4bde1425cb1',
    '0c3d304d498053cea945ad98503a2cbfc208b25dc5ca692ad7f6a2490ca6457e',
  ])],
  ['006_create_room_players.sql', new Set([
    '6f156d21d69a4295dc6cee2340232b8e80fd71a6fba6bb1b3f9428c6cd5e0ec2',
    'be6c5690ea14e9e1f36132947446f95ce90a06bc23420ebc732ff0bb9b7284f4',
  ])],
  ['007_create_game_saves.sql', new Set([
    '6df3e168901e91936a3b7bb87137ad4f9958ce0992a064e35d6c6da900d343f',
    'c7b7b5536ff432f631782b0957b9a786e9a4c2f253fc74418a6468f5bdf8020c',
  ])],
]);

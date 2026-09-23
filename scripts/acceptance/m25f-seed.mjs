// Seed an isolated migrated database for the M2.5-F browser acceptance run.
import mysql from 'mysql2/promise'

const connection = await mysql.createConnection({
  host: process.env.M25F_DB_HOST || '127.0.0.1',
  port: Number(process.env.M25F_DB_PORT || 23306),
  user: process.env.M25F_DB_USER || 'trpg',
  password: process.env.M25F_DB_PASSWORD || 'acceptance-pass',
  database: 'trpggame'
})

try {
  const [[owner]] = await connection.execute('SELECT id FROM users WHERE username = ?', ['m25f_owner'])
  if (!owner) throw new Error('Register m25f_owner before seeding')
  await connection.beginTransaction()
  const [scriptResult] = await connection.execute(
    "INSERT INTO scripts (user_id, title, description, file_path, status) VALUES (?, ?, ?, ?, 'ready')",
    [owner.id, 'M2.5-F 验收钟楼', '隔离环境中的确定性多人验收剧本', 'acceptance/m25f-fixture.pdf']
  )
  for (let index = 1; index <= 6; index++) {
    await connection.execute(
      'INSERT INTO script_characters (script_id, name, description, attributes) VALUES (?, ?, ?, ?)',
      [scriptResult.insertId, `验收角色 ${index}`, '多人回合验收角色', JSON.stringify({ hp: 12, mp: 6, san: 10 })]
    )
  }
  await connection.commit()
  process.stdout.write(`Seeded acceptance script ${scriptResult.insertId} with 6 characters\n`)
} catch (error) {
  await connection.rollback()
  throw error
} finally {
  await connection.end()
}

-- Codes 90/91/92 distinguish a closed gate, stale branch and corrupt ledger.
-- These keys are deliberately outside room:<id> runtime cleanup and TTL lists.
local function archive_type(key, wanted)
  local actual = redis.call('TYPE', key).ok
  return actual == 'none' or actual == wanted
end

local function archive_number(value)
  return string.format('%.0f', value)
end

local function archive_meta(meta_key, pending_key)
  local kind = redis.call('TYPE', meta_key).ok
  if kind == 'none' then
    if redis.call('EXISTS', pending_key) ~= 0 then return nil, 92 end
    return nil, 0
  end
  if kind ~= 'hash' or not archive_type(pending_key, 'hash') then return nil, 92 end
  local values = redis.call('HMGET', meta_key, 'timeline_id', 'head_position', 'durable_position',
    'archive_state', 'control_operation_id', 'revision', 'mode', 'timeout_ms')
  local head, durable, revision, timeout = tonumber(values[2]), tonumber(values[3]), tonumber(values[6]), tonumber(values[8])
  if not values[1] or #values[1] ~= 36 or not head or not durable or head < durable or head - durable > 1 or
     head < 0 or head > 9007199254740990 or head % 1 ~= 0 or durable % 1 ~= 0 or
     not revision or revision < 1 or revision > 9007199254740990 or revision % 1 ~= 0 or
     (values[7] ~= 'solo' and values[7] ~= 'multiplayer') or not timeout or timeout < 0 or
     (values[7] == 'multiplayer' and (timeout < 1000 or timeout > 3600000)) or
     (values[7] == 'solo' and timeout ~= 0) or not values[5] then return nil, 92 end
  if values[4] ~= 'ready' and values[4] ~= 'pending' and values[4] ~= 'recovering' and values[4] ~= 'blocked' then return nil, 92 end
  return {timeline = values[1], head = head, durable = durable, state = values[4], control = values[5],
    revision = revision, mode = values[7], timeout = timeout}, 0
end

local function archive_gate(meta_key, pending_key, timeline, mode, replay)
  local meta, code = archive_meta(meta_key, pending_key)
  if code ~= 0 then return nil, code end
  if not meta then
    if timeline ~= '' then return nil, 91 end
    return nil, 0
  end
  if timeline == '' or meta.timeline ~= timeline or meta.mode ~= mode then return nil, 91 end
  if not replay and (meta.state ~= 'ready' or meta.control ~= '' or meta.head ~= meta.durable or
     redis.call('EXISTS', pending_key) ~= 0) then return nil, 90 end
  return meta, 0
end

local function archive_prepare(keys, encoded, mode, generation, room_id, actor_id, turn, round_before, round_after, request_id, fingerprint, expected_kind)
  local meta_key, pending_key, lease_key, queue_key = unpack(keys)
  if not archive_type(lease_key, 'hash') or not archive_type(queue_key, 'zset') or
     not archive_type(keys[5], 'hash') or not archive_type(keys[6], 'zset') then return nil, 92 end
  local envelope, record
  if encoded ~= '' then
    local ok
    ok, envelope = pcall(cjson.decode, encoded)
    if not ok or type(envelope) ~= 'table' or type(envelope.record_json) ~= 'string' or
       #envelope.record_json > 1056768 then return nil, 92 end
    ok, record = pcall(cjson.decode, envelope.record_json)
    if not ok or type(record) ~= 'table' then return nil, 92 end
  end
  local meta, code = archive_gate(meta_key, pending_key, record and record.TimelineID or '', mode, false)
  if code ~= 0 then return nil, code end
  if not meta then return nil, 0 end
  if not record or record.RoomID ~= tonumber(room_id) or record.SourceGeneration ~= generation or
     record.Position ~= meta.head + 1 or record.SourceRevision ~= meta.revision or record.Kind ~= expected_kind or
     record.TurnBefore ~= turn or record.TurnAfter ~= turn + 1 or record.RoundBefore ~= round_before or record.RoundAfter ~= round_after or
     record.RequestID ~= request_id or record.Fingerprint ~= fingerprint or record.ActorID ~= tonumber(actor_id) or
     type(record.CommitID) ~= 'string' or #record.CommitID ~= 36 or type(record.PayloadHash) ~= 'string' or #record.PayloadHash ~= 64 then return nil, 91 end
  return {meta = meta, record = record, encoded = envelope.record_json, keys = keys}, 0
end

local function archive_replay(keys, encoded, mode, cached)
  local envelope, record
  if encoded ~= '' then
    local ok
    ok, envelope = pcall(cjson.decode, encoded)
    if not ok or type(envelope) ~= 'table' or type(envelope.record_json) ~= 'string' then return 92 end
    ok, record = pcall(cjson.decode, envelope.record_json)
    if not ok or type(record) ~= 'table' then return 92 end
  end
  local meta, code = archive_gate(keys[1], keys[2], record and record.TimelineID or '', mode, true)
  if code ~= 0 then return code end
  if not meta then return 0 end
  local ok, value = pcall(cjson.decode, cached)
  if not ok or type(value) ~= 'table' or not record or value.archive_timeline_id ~= meta.timeline then return 91 end
  if value.archive_commit_id ~= record.CommitID or value.archive_hash ~= record.PayloadHash then return 93 end
  return 0
end

local function archive_commit(plan, response, boundary)
  if not plan then return end
  redis.call('HSET', plan.keys[1], 'head_position', archive_number(plan.record.Position), 'archive_state', 'pending')
  redis.call('HSET', plan.keys[2], 'record_json', plan.encoded, 'response_json', response, 'boundary_snapshot', boundary or '',
    'commit_id', plan.record.CommitID, 'payload_hash', plan.record.PayloadHash, 'timeline_id', plan.meta.timeline,
    'position', archive_number(plan.record.Position))
  redis.call('ZADD', plan.keys[4], 0, archive_number(plan.record.RoomID))
end

-- Build the original automatic-save boundary before any gameplay write.
local function archive_encode_snapshot(snapshot)
  local ok, encoded = pcall(cjson.encode, snapshot)
  if not ok or #encoded > 1056768 then return '', 92 end
  encoded = string.gsub(encoded, '"items":%[null%]', '"items":[]')
  encoded = string.gsub(encoded, '"buffs":%[null%]', '"buffs":[]')
  return encoded, 0
end

local function archive_memory(plan)
  return {timeline_id = plan.meta.timeline, head_position = plan.record.Position,
    durable_position = plan.meta.durable, archive_state = 'pending', revision = plan.meta.revision, control_operation_id = ''}
end

local function archive_messages(key, first, second)
  if not archive_type(key, 'list') then return nil end
  local raw = redis.call('LRANGE', key, 0, 9)
  if first then table.insert(raw, 1, first); table.insert(raw, 1, second) end
  while #raw > 10 do table.remove(raw) end
  local messages = {}
  for index = #raw, 1, -1 do
    local ok, message = pcall(cjson.decode, raw[index])
    if not ok or type(message) ~= 'table' or type(message.role) ~= 'string' or type(message.content) ~= 'string' then return nil end
    table.insert(messages, message)
  end
  return messages
end

local function archive_solo_boundary(keys, args, turn, change_count, inventory, buff_args, plan)
  if redis.call('TYPE', keys[3]).ok ~= 'list' or redis.call('TYPE', keys[4]).ok ~= 'string' then return '', 92 end
  local order = {}
  for _, text in ipairs(redis.call('LRANGE', keys[3], 0, -1)) do
    local id = tonumber(text); if not id then return '', 92 end; table.insert(order, id)
  end
  if #order ~= 1 then return '', 92 end
  local state = {}; local raw = redis.call('HGETALL', keys[6])
  for i = 1, #raw, 2 do state[raw[i]] = raw[i+1] end
  for i = 0, change_count - 1 do state[args[8+i*2]] = args[9+i*2] end
  local items = {}
  for name, value in pairs(inventory) do table.insert(items, {name = name, quantity = value.quantity, description = value.description}) end
  table.sort(items, function(a,b) return a.name < b.name end)
  if #items == 0 then items = {cjson.null} end
  local buffs_map = {}; raw = redis.call('HGETALL', keys[9])
  for i = 1, #raw, 2 do buffs_map[raw[i]] = tonumber(raw[i+1]) end
  for i = 1, #buff_args, 2 do buffs_map[buff_args[i]] = tonumber(buff_args[i+1]) end
  local buffs = {}; for name, duration in pairs(buffs_map) do table.insert(buffs, {name=name, duration=duration}) end
  table.sort(buffs, function(a,b) return a.name < b.name end)
  if #buffs == 0 then buffs = {cjson.null} end
  local messages = archive_messages(keys[5], args[5], args[6]); if not messages then return '', 92 end
  local newest_first = {}; for index = #messages, 1, -1 do table.insert(newest_first, messages[index]) end
  return archive_encode_snapshot({version=1, room_id=plan.record.RoomID, user_id=plan.record.ActorID, status='playing', turn=turn,
    turn_order=order, player_state=state, items=items, buffs=buffs, summary=redis.call('GET', keys[4]), recent_messages=newest_first,
    memory=archive_memory(plan)})
end

local function archive_multiplayer_boundary(room_id, generation, turn, order_key, players_key, rounds_key, summary_key, plans, first, second, plan)
  if not archive_type(players_key, 'list') or redis.call('TYPE', summary_key).ok ~= 'string' then return '', 92 end
  local order = {}; for _, text in ipairs(redis.call('LRANGE', order_key, 0, -1)) do table.insert(order, tonumber(text)) end
  local round = math.floor(turn / #order)
  if turn % #order ~= 0 or round <= 0 or round % 5 ~= 0 then return '', 0 end
  local updates = {}; for _, value in ipairs(plans) do updates[value.mutation.user_id] = value end
  local players = {}
  for _, user_text in ipairs(redis.call('LRANGE', players_key, 0, -1)) do
    local user = tonumber(user_text); local key = 'room:' .. tostring(room_id) .. ':player:' .. user_text
    if redis.call('TYPE', key).ok ~= 'hash' or not archive_type(key..':items', 'set') or not archive_type(key..':buffs', 'hash') then return '', 92 end
    local state = {}; local raw = redis.call('HGETALL', key)
    for i = 1, #raw, 2 do state[raw[i]] = raw[i+1] end
    local character = tonumber(state.character_id); state.character_id = nil
    if not character or character < 1 then return '', 92 end
    local update = updates[user]; local inventory = {}
    if update then
      inventory = update.inventory
      for field, value in pairs(update.mutation.player_state_changes or {}) do state[field] = value end
    else
      for _, encoded in ipairs(redis.call('SMEMBERS', key..':items')) do
        local ok, item = pcall(cjson.decode, encoded)
        if not ok or type(item) ~= 'table' or type(item.name) ~= 'string' or type(item.quantity) ~= 'number' or item.quantity <= 0 or inventory[item.name] then return '', 92 end
        inventory[item.name] = item
      end
    end
    local items = {}; for name, value in pairs(inventory) do table.insert(items, {name=name, quantity=value.quantity, description=value.description or ''}) end
    table.sort(items, function(a,b) return a.name < b.name end); if #items == 0 then items = {cjson.null} end
    local buffs_map = {}; raw = redis.call('HGETALL', key..':buffs')
    for i = 1, #raw, 2 do
      local duration = tonumber(raw[i+1]); if not duration or duration <= 0 or duration % 1 ~= 0 then return '', 92 end
      buffs_map[raw[i]] = duration
    end
    if update then for _, buff in ipairs(update.mutation.buffs or {}) do buffs_map[buff.name] = buff.duration end end
    local buffs = {}; for name, duration in pairs(buffs_map) do table.insert(buffs, {name=name, duration=duration}) end
    table.sort(buffs, function(a,b) return a.name < b.name end); if #buffs == 0 then buffs = {cjson.null} end
    table.insert(players, {user_id=user, character_id=character, player_state=state, items=items, buffs=buffs})
  end
  if #players ~= #order then return '', 92 end
  local messages = archive_messages(rounds_key, first, second); if not messages then return '', 92 end
  return archive_encode_snapshot({version=2, room_id=tonumber(room_id), status='playing', generation=generation, current_turn=turn,
    round_number=round, turn_order=order, current_actor_id=order[turn % #order + 1], deadline_at=cjson.null,
    players=players, summary_memory=redis.call('GET', summary_key), recent_messages=messages, memory=archive_memory(plan)})
end

-- Runtime replacement is an atomic, token-fenced operation. All write plans
-- come from validated Go snapshots; no caller-provided Redis keys are accepted.
local action = ARGV[1]
local ok, p = pcall(cjson.decode, ARGV[2])
if not ok or type(p) ~= 'table' then return {92} end
local function key(index) return KEYS[index] end
local function typed(index, wanted) return archive_type(key(index), wanted) end
local function current()
  return archive_meta(KEYS[1], KEYS[2])
end
local function reply(code) return {code} end
local function control()
  if not typed(7,'hash') or not typed(8,'hash') then return nil end
  return redis.call('HGET',KEYS[7],'operation_id')
end
local function same_control()
  return control() == p.operation_id and redis.call('HGET',KEYS[7],'snapshot_hash') == p.snapshot_hash
end
local function receipt()
  if not typed(8,'hash') then return nil end
  return redis.call('HGET',KEYS[8],p.operation_id)
end
local function stats(phase,hash)
  return {1,cjson.encode({phase=phase,snapshot_hash=hash,
    source_turn=tonumber(redis.call('HGET',KEYS[7],'source_turn') or '0'),
    source_round=tonumber(redis.call('HGET',KEYS[7],'source_round') or '0')})}
end
local function preflight()
  for i, kind in ipairs({'hash','hash','hash','zset','hash','zset','hash','hash','zset','zset'}) do
    if not typed(i,kind) then return false end
  end
  for _,w in ipairs(p.writes or {}) do
    if type(w.index)~='number' or w.index<11 or w.index>#KEYS or not typed(w.index,w.kind) or
       type(w.values)~='table' or (w.kind=='hash' and #w.values%2~=0) then return false end
  end
  for _,index in ipairs(p.clear or {}) do if index<11 or index>#KEYS then return false end end
  return true
end
local function clear_deadline()
  local gen=redis.call('GET',key(p.generation_key))
  local turn=redis.call('GET',key(p.turn_key))
  if gen and turn then redis.call('ZREM',KEYS[9],p.room_id..':'..gen..':'..turn) end
  if p.deadline_key then redis.call('DEL',key(p.deadline_key)) end
  if p.lease_key then redis.call('DEL',key(p.lease_key)) end
end
local function write_image(status)
  clear_deadline()
  for _,index in ipairs(p.clear) do redis.call('DEL',key(index)) end
  for _,w in ipairs(p.writes) do
    if w.kind=='string' then redis.call('SET',key(w.index),w.values[1])
    elseif #w.values>0 then
      if w.kind=='hash' then for i=1,#w.values,2 do redis.call('HSET',key(w.index),w.values[i],w.values[i+1]) end
      elseif w.kind=='list' then for _,v in ipairs(w.values) do redis.call('RPUSH',key(w.index),v) end
      elseif w.kind=='set' then for _,v in ipairs(w.values) do redis.call('SADD',key(w.index),v) end end
    end
  end
  redis.call('SET',key(p.status_key),status)
  for _,index in ipairs(p.clear) do
    if redis.call('EXISTS',key(index))==1 then redis.call('PEXPIRE',key(index),p.ttl_ms) end
  end
end

if action=='source' then
  local meta,code=current();if code~=0 then return reply(code) end
  if not meta then return reply(0) end
  local gen=redis.call('GET',key(p.generation_key))
  local status=redis.call('GET',key(p.status_key))
  local turn=tonumber(redis.call('GET',key(p.turn_key)))
  local count=redis.call('LLEN',key(p.order_key))
  if not gen or not status or not turn or count<1 then return reply(90) end
  return {1,redis.call('HGETALL',KEYS[1]),gen,status,archive_number(turn),archive_number(math.floor(turn/count))}
end
if action=='inspect' then
  if not typed(7,'hash') or not typed(8,'hash') then return reply(92) end
  local saved=receipt()
  if saved then return stats('finished',saved) end
  if not same_control() then return reply(91) end
  return stats(redis.call('HGET',KEYS[7],'phase'),p.snapshot_hash)
end
if not preflight() then return reply(92) end
local saved=receipt()
if saved then if saved==p.snapshot_hash then return reply(2) else return reply(93) end end
local meta,code=current();if code~=0 then return reply(code) end

if action=='initialize' then
  if p.kind~='start' then return reply(91) end
  if same_control() then return reply(2) end
  if control() or meta or redis.call('EXISTS',key(p.status_key))~=0 or
     redis.call('EXISTS',key(p.generation_key))~=0 then return reply(91) end
  write_image('provisional')
  redis.call('HSET',KEYS[1],'timeline_id',p.timeline_id,'head_position','0','durable_position','0',
    'revision',p.revision,'mode',p.mode,'timeout_ms',p.timeout_ms,'archive_state','recovering','control_operation_id',p.operation_id)
  redis.call('HSET',KEYS[7],'operation_id',p.operation_id,'snapshot_hash',p.snapshot_hash,'kind','start',
    'phase','fenced','source_turn','0','source_round','0')
  return reply(1)
end
if action=='abort_start' then
  if p.kind~='start' or not meta or not same_control() or meta.control~=p.operation_id then return reply(91) end
  local phase=redis.call('HGET',KEYS[7],'phase')
  if phase=='aborted' then return reply(2) end
  if phase~='fenced' or meta.head~=0 or meta.durable~=0 or redis.call('EXISTS',KEYS[2])~=0 then return reply(91) end
  clear_deadline()
  for _,index in ipairs(p.clear) do redis.call('DEL',key(index)) end
  redis.call('HSET',KEYS[1],'archive_state','blocked')
  redis.call('HSET',KEYS[7],'phase','aborted')
  return reply(1)
end
if not meta or meta.state=='blocked' then return reply(90) end
if action=='fence' then
  if same_control() and meta.control==p.operation_id then return reply(2) end
  if control() and redis.call('HGET',KEYS[7],'phase')~='finished' then return reply(90) end
  if meta.control~='' or meta.timeline~=p.source_timeline or meta.revision~=tonumber(p.source_revision) or meta.mode~=p.mode then return reply(91) end
  local gen=redis.call('GET',key(p.generation_key))
  local status=redis.call('GET',key(p.status_key))
  local turn=tonumber(redis.call('GET',key(p.turn_key)))
  local count=redis.call('LLEN',key(p.order_key))
  local lost=not gen and not status and not turn
  if lost then
    if not p.recovery or p.kind~='load' then return reply(90) end
    turn=0;count=1
  elseif gen~=p.source_generation or (status~='playing' and status~='paused') or not turn or count<1 then return reply(91) end
  clear_deadline()
  redis.call('SET',key(p.status_key),'paused','PX',p.ttl_ms)
  redis.call('SET',key(p.generation_key),p.fence_generation,'PX',p.ttl_ms)
  redis.call('HSET',KEYS[1],'control_operation_id',p.operation_id,'archive_state','recovering')
  redis.call('DEL',KEYS[7])
  redis.call('HSET',KEYS[7],'operation_id',p.operation_id,'snapshot_hash',p.snapshot_hash,'kind',p.kind,'phase','fenced',
    'source_turn',archive_number(turn),'source_round',archive_number(math.floor(turn/count)),'source_head',archive_number(meta.head))
  return reply(1)
end
if not same_control() or meta.control~=p.operation_id then return reply(91) end
if action=='apply' then
  local phase=redis.call('HGET',KEYS[7],'phase')
  if phase~='fenced' and phase~='applied' then return reply(91) end
  if meta.head~=meta.durable or redis.call('EXISTS',KEYS[2])~=0 or redis.call('HLEN',KEYS[5])~=0 then return reply(90) end
  local gen=redis.call('GET',key(p.generation_key))
  local status=redis.call('GET',key(p.status_key))
  if phase=='applied' then
    if p.kind=='end' then return reply(2) end
    if gen==p.generation and (status=='playing' or status=='paused') then
      if not (p.kind=='start' and p.recovery and status=='playing') then return reply(2) end
    elseif not p.recovery or gen or status then return reply(91) end
  elseif p.kind=='start' then
    if (gen~=p.generation or status~='provisional') and not (p.recovery and not gen and not status) then return reply(91) end
  elseif gen~=p.fence_generation or status~='paused' then
    if not p.recovery or gen or status then return reply(91) end
  end
  if p.kind~='end' then
    local target_status='paused'
    if p.kind=='start' and not p.recovery then target_status='playing' end
    write_image(target_status)
    redis.call('HSET',KEYS[1],'timeline_id',p.timeline_id,'head_position',p.position,'durable_position',p.position,'revision',p.revision)
  end
  redis.call('HSET',KEYS[7],'phase','applied')
  return reply(1)
end
if action=='finish' then
  if redis.call('HGET',KEYS[7],'phase')~='applied' then return reply(91) end
  if p.kind~='end' then
    local status=redis.call('GET',key(p.status_key))
    if redis.call('GET',key(p.generation_key))~=p.generation or (status~='playing' and status~='paused') then return reply(90) end
    -- A TTL gap cannot publish a usable capability with a missing target image.
    for _,w in ipairs(p.writes) do
      if w.kind=='string' then
        if redis.call('GET',key(w.index))~=w.values[1] then return reply(90) end
      elseif #w.values>0 and redis.call('EXISTS',key(w.index))~=1 then return reply(90) end
    end
  end
  if p.kind=='start' then
    local record_ok,record=pcall(cjson.decode,ARGV[3] or '')
    if not record_ok or type(record)~='table' or record.Kind~='opening' or record.Position~=1 or record.TimelineID~=meta.timeline or
       record.SourceRevision~=meta.revision or record.SourceGeneration~=p.generation then return reply(92) end
    if redis.call('EXISTS',KEYS[2])~=0 then
      if redis.call('HGET',KEYS[2],'record_json')~=ARGV[3] or meta.head~=1 or meta.durable~=0 then return reply(92) end
    elseif meta.head==0 and meta.durable==0 then
      redis.call('HSET',KEYS[2],'record_json',ARGV[3],'response_json',ARGV[4],'boundary_snapshot','',
        'commit_id',record.CommitID,'payload_hash',record.PayloadHash,'timeline_id',record.TimelineID,'position','1')
      redis.call('HSET',KEYS[1],'head_position','1')
      redis.call('ZADD',KEYS[4],0,p.room_id)
    else return reply(92) end
    redis.call('HSET',KEYS[1],'archive_state','pending','control_operation_id','')
  else
    if meta.head~=meta.durable or redis.call('EXISTS',KEYS[2])~=0 or redis.call('HLEN',KEYS[5])~=0 then return reply(90) end
    if p.kind=='load' and (meta.timeline~=p.timeline_id or meta.revision~=tonumber(p.revision)) then return reply(91) end
    if p.kind=='end' then
      clear_deadline()
      for _,index in ipairs(p.clear) do redis.call('DEL',key(index)) end
    end
    redis.call('HSET',KEYS[1],'archive_state','ready','control_operation_id','')
  end
  redis.call('HSET',KEYS[7],'phase','finished')
  redis.call('HSET',KEYS[8],p.operation_id,p.snapshot_hash)
  redis.call('ZREM',KEYS[10],p.room_id)
  return reply(1)
end
return reply(92)

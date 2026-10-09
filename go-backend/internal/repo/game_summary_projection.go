package repo

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
)

var summaryProjectionScript = redis.NewScript(`
local meta=redis.call('HMGET',KEYS[1],'timeline_id','revision','durable_position','archive_state','control_operation_id')
if meta[1]~=ARGV[1] or meta[2]~=ARGV[2] or meta[4]~='ready' or (meta[5] and meta[5]~='') or not tonumber(meta[3]) or tonumber(meta[3])<tonumber(ARGV[4]) then return 0 end
if redis.call('EXISTS',KEYS[4])==0 then return 0 end
local status=redis.call('GET',KEYS[5])
if status~='playing' and status~='paused' then return 0 end
local prior=redis.call('HMGET',KEYS[3],'timeline_id','version','through')
if prior[1]==ARGV[1] and (tonumber(prior[2] or '0')>tonumber(ARGV[3]) or tonumber(prior[3] or '0')>tonumber(ARGV[4])) then return 0 end
local ttl=redis.call('PTTL',KEYS[4])
if ttl==0 then return 0 end
local summary_ttl=redis.call('PTTL',KEYS[2])
if summary_ttl>0 and (ttl<0 or summary_ttl<ttl) then ttl=summary_ttl end
redis.call('SET',KEYS[2],ARGV[5])
redis.call('HSET',KEYS[3],'timeline_id',ARGV[1],'version',ARGV[3],'through',ARGV[4])
if ttl>0 then redis.call('PEXPIRE',KEYS[2],ttl);redis.call('PEXPIRE',KEYS[3],ttl) end
return 1
`)

func (r *RedisGameStateRepo) PublishSummary(ctx context.Context, room uint, timeline string, revision, version, through uint64, summary string) error {
	_, err := summaryProjectionScript.Run(ctx, r.client, []string{gameArchiveKeys(room)[0], fmt.Sprintf("room:%d:summary", room), fmt.Sprintf("room:%d:summary_meta", room), runtimeGenerationKey(room), runtimeStatusKey(room)}, timeline, revision, version, through, summary).Result()
	return err
}

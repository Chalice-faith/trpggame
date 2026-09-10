package repo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const DefaultPresenceTTL = 90 * time.Second

var ErrInvalidPresenceLease = errors.New("invalid presence lease")

var claimPresenceScript = redis.NewScript(`
local existed = redis.call('EXISTS', KEYS[1])
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
if existed == 0 then return 1 else return 0 end`)

var refreshPresenceScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0`)

var releasePresenceScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

// PresenceRepo 使用 connection ID + TTL 管理 IM 在线租约。
type PresenceRepo struct {
	client *redis.Client
	ttl    time.Duration
}

func NewPresenceRepo(client *redis.Client, ttl time.Duration) (*PresenceRepo, error) {
	if client == nil || ttl < time.Millisecond {
		return nil, ErrInvalidPresenceLease
	}
	return &PresenceRepo{client: client, ttl: ttl}, nil
}

// Claim 原子覆盖当前连接租约；返回此前是否为离线。
func (r *PresenceRepo) Claim(ctx context.Context, userID uint, connectionID string) (bool, error) {
	if userID == 0 || connectionID == "" {
		return false, ErrInvalidPresenceLease
	}
	result, err := claimPresenceScript.Run(ctx, r.client, []string{presenceKey(userID)}, connectionID, r.ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("claim presence: %w", err)
	}
	return result == 1, nil
}

// Refresh 仅为仍持有租约的 connection ID 续期。
func (r *PresenceRepo) Refresh(ctx context.Context, userID uint, connectionID string) (bool, error) {
	if userID == 0 || connectionID == "" {
		return false, ErrInvalidPresenceLease
	}
	result, err := refreshPresenceScript.Run(ctx, r.client, []string{presenceKey(userID)}, connectionID, r.ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("refresh presence: %w", err)
	}
	return result == 1, nil
}

// Release 仅删除仍由该 connection ID 持有的租约。
func (r *PresenceRepo) Release(ctx context.Context, userID uint, connectionID string) (bool, error) {
	if userID == 0 || connectionID == "" {
		return false, ErrInvalidPresenceLease
	}
	result, err := releasePresenceScript.Run(ctx, r.client, []string{presenceKey(userID)}, connectionID).Int64()
	if err != nil {
		return false, fmt.Errorf("release presence: %w", err)
	}
	return result == 1, nil
}

// Statuses 返回每个用户是否拥有有效租约；Redis 错误由调用方映射为 unknown。
func (r *PresenceRepo) Statuses(ctx context.Context, userIDs []uint) (map[uint]bool, error) {
	result := make(map[uint]bool, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	keys := make([]string, len(userIDs))
	for index, userID := range userIDs {
		if userID == 0 {
			return nil, ErrInvalidPresenceLease
		}
		keys[index] = presenceKey(userID)
	}
	values, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("get presence statuses: %w", err)
	}
	for index, userID := range userIDs {
		result[userID] = values[index] != nil
	}
	return result, nil
}

func presenceKey(userID uint) string {
	return "presence:user:" + strconv.FormatUint(uint64(userID), 10) + ":im"
}

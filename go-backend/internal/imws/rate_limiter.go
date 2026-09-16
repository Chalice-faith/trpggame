package imws

import "time"

const (
	businessTokensPerSecond = 10.0
	businessTokenCapacity   = 20.0
)

// weightedTokenBucket 是单连接、单读取协程使用的加权令牌桶。
// time.Time.Sub 会保留 Go 单调时钟语义，系统时间回拨不会额外补充令牌。
type weightedTokenBucket struct {
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newWeightedTokenBucket(rate, capacity float64, now func() time.Time) *weightedTokenBucket {
	if now == nil {
		now = time.Now
	}
	current := now()
	return &weightedTokenBucket{
		rate: rate, capacity: capacity, tokens: capacity, last: current, now: now,
	}
}

func newBusinessTokenBucket() *weightedTokenBucket {
	return newWeightedTokenBucket(businessTokensPerSecond, businessTokenCapacity, time.Now)
}

func (b *weightedTokenBucket) allow(cost float64) bool {
	if b == nil || cost <= 0 {
		return true
	}
	current := b.now()
	if elapsed := current.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = current
	}
	if b.tokens < cost {
		return false
	}
	b.tokens -= cost
	return true
}

func businessMessageCost(messageType MessageType) float64 {
	switch messageType {
	case MsgChatMessage:
		return 1
	case MsgImSync:
		return 5
	default:
		return 0
	}
}

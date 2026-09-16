package imws

import (
	"testing"
	"time"
)

func TestWeightedTokenBucketBurstRefillAndCosts(t *testing.T) {
	current := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	bucket := newWeightedTokenBucket(10, 20, func() time.Time { return current })

	for index := 0; index < 20; index++ {
		if !bucket.allow(businessMessageCost(MsgChatMessage)) {
			t.Fatalf("burst token %d rejected", index+1)
		}
	}
	if bucket.allow(businessMessageCost(MsgChatMessage)) {
		t.Fatal("request beyond burst capacity was allowed")
	}

	current = current.Add(100 * time.Millisecond)
	if !bucket.allow(businessMessageCost(MsgChatMessage)) {
		t.Fatal("one token was not restored after 100ms")
	}
	if bucket.allow(businessMessageCost(MsgChatMessage)) {
		t.Fatal("more than one token was restored after 100ms")
	}

	current = current.Add(10 * time.Second)
	for index := 0; index < 4; index++ {
		if !bucket.allow(businessMessageCost(MsgImSync)) {
			t.Fatalf("sync request %d rejected with full bucket", index+1)
		}
	}
	if bucket.allow(businessMessageCost(MsgImSync)) {
		t.Fatal("fifth sync request exceeded the 20-token capacity")
	}
}

func TestWeightedTokenBucketIgnoresPingAndUnknownTypes(t *testing.T) {
	current := time.Now()
	bucket := newWeightedTokenBucket(10, 20, func() time.Time { return current })
	for index := 0; index < 100; index++ {
		if !bucket.allow(businessMessageCost(MsgPing)) || !bucket.allow(businessMessageCost(MessageType("future_event"))) {
			t.Fatal("zero-cost message was rejected")
		}
	}
	for index := 0; index < 20; index++ {
		if !bucket.allow(1) {
			t.Fatalf("zero-cost messages consumed token %d", index+1)
		}
	}
}

func TestWeightedTokenBucketIsPerConnection(t *testing.T) {
	now := time.Now
	first := newWeightedTokenBucket(10, 20, now)
	for index := 0; index < 20; index++ {
		if !first.allow(1) {
			t.Fatalf("first bucket rejected token %d", index+1)
		}
	}
	if first.allow(1) {
		t.Fatal("first bucket should be exhausted")
	}
	second := newWeightedTokenBucket(10, 20, now)
	if !second.allow(5) {
		t.Fatal("new connection bucket did not start full")
	}
}

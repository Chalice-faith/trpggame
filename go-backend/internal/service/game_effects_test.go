package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestInterpretActionEffectsNormalizesSupportedCalls(t *testing.T) {
	effects, err := InterpretActionEffects(7, effectChanges(
		effectCall("update_player_status", map[string]any{
			"player_id": 7, "field": "hp", "value": 8, "reason": " 受伤 ",
		}),
		effectCall("set_location", map[string]any{
			"player_id": 7, "location": " 书房 ",
		}),
		effectCall("add_item", map[string]any{
			"player_id": 7, "item_name": " 银钥匙 ", "quantity": 2, "description": " 生锈 ",
		}),
		effectCall("remove_item", map[string]any{
			"player_id": 7, "item_name": " 火把 ", "quantity": 1,
		}),
		effectCall("add_buff", map[string]any{
			"player_id": 7, "buff_name": " 中毒 ", "duration": 3,
		}),
		effectCall("trigger_event", map[string]any{
			"event_name": " 密门开启 ", "description": " 玩家打开了书房密门。 ",
		}),
	))
	if err != nil {
		t.Fatalf("InterpretActionEffects() error = %v", err)
	}
	if !reflect.DeepEqual(effects.PlayerStateChanges, map[string]string{
		"hp": "8", "location": "书房",
	}) {
		t.Fatalf("player state changes = %#v", effects.PlayerStateChanges)
	}
	if !reflect.DeepEqual(effects.Items, []ItemMutation{
		{Name: "银钥匙", QuantityDelta: 2, Description: "生锈"},
		{Name: "火把", QuantityDelta: -1},
	}) {
		t.Fatalf("item mutations = %#v", effects.Items)
	}
	if !reflect.DeepEqual(effects.Buffs, []BuffMutation{{Name: "中毒", Duration: 3}}) {
		t.Fatalf("buff mutations = %#v", effects.Buffs)
	}
	if !reflect.DeepEqual(effects.Events, []KeyEventMutation{{
		Name: "密门开启", Description: "玩家打开了书房密门。",
	}}) {
		t.Fatalf("event mutations = %#v", effects.Events)
	}
}

func TestInterpretActionEffectsUsesLastAbsolutePlayerStateValue(t *testing.T) {
	effects, err := InterpretActionEffects(7, effectChanges(
		effectCall("update_player_status", map[string]any{
			"player_id": 7, "field": "san", "value": 50, "reason": "惊吓",
		}),
		effectCall("update_player_status", map[string]any{
			"player_id": 7, "field": "san", "value": 45, "reason": "再次惊吓",
		}),
	))
	if err != nil {
		t.Fatalf("InterpretActionEffects() error = %v", err)
	}
	if effects.PlayerStateChanges["san"] != "45" {
		t.Fatalf("san = %q, want 45", effects.PlayerStateChanges["san"])
	}
}

func TestInterpretActionEffectsAllowsNoChanges(t *testing.T) {
	effects, err := InterpretActionEffects(0, nil)
	if err != nil {
		t.Fatalf("InterpretActionEffects() error = %v", err)
	}
	if effects == nil || len(effects.PlayerStateChanges) != 0 ||
		len(effects.Items) != 0 || len(effects.Buffs) != 0 || len(effects.Events) != 0 {
		t.Fatalf("empty effects = %#v", effects)
	}
}

func TestInterpretActionEffectsRejectsMalformedEnvelope(t *testing.T) {
	tests := []map[string]any{
		{},
		{"calls": []any{}},
		{"calls": "not-an-array"},
		{"calls": []any{}, "unexpected": true},
		{"calls": []any{map[string]any{"name": "set_location"}}},
		{"calls": []any{map[string]any{
			"name": "set_location", "arguments": map[string]any{"player_id": 7, "location": "书房"}, "extra": true,
		}}},
	}
	for index, changes := range tests {
		if _, err := InterpretActionEffects(7, changes); !errors.Is(err, ErrInvalidActionEffects) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
	if _, err := InterpretActionEffects(0, effectChanges(effectCall("set_location", map[string]any{
		"player_id": 7, "location": "书房",
	}))); !errors.Is(err, ErrInvalidActionEffects) {
		t.Fatalf("zero user ID error = %v", err)
	}

	tooMany := make([]any, maxActionEffectCalls+1)
	for index := range tooMany {
		tooMany[index] = effectCall("set_location", map[string]any{
			"player_id": 7, "location": "书房",
		})
	}
	if _, err := InterpretActionEffects(7, map[string]any{"calls": tooMany}); !errors.Is(err, ErrInvalidActionEffects) {
		t.Fatalf("too many calls error = %v", err)
	}
}

func TestInterpretActionEffectsRejectsUnsafeCalls(t *testing.T) {
	tests := []struct {
		name string
		call map[string]any
	}{
		{"unknown effect", effectCall("delete_room", map[string]any{})},
		{"dice is not state", effectCall("roll_dice", map[string]any{})},
		{"another player", effectCall("set_location", map[string]any{"player_id": 8, "location": "书房"})},
		{"extra argument", effectCall("set_location", map[string]any{"player_id": 7, "location": "书房", "admin": true})},
		{"fractional integer", effectCall("update_player_status", map[string]any{"player_id": 7, "field": "hp", "value": 1.5, "reason": ""})},
		{"unknown status", effectCall("update_player_status", map[string]any{"player_id": 7, "field": "character_id", "value": 1, "reason": ""})},
		{"zero item quantity", effectCall("add_item", map[string]any{"player_id": 7, "item_name": "钥匙", "quantity": 0, "description": ""})},
		{"item delimiter", effectCall("add_item", map[string]any{"player_id": 7, "item_name": "钥匙|伪造", "quantity": 1, "description": ""})},
		{"zero buff duration", effectCall("add_buff", map[string]any{"player_id": 7, "buff_name": "中毒", "duration": 0})},
		{"empty event", effectCall("trigger_event", map[string]any{"event_name": "事件", "description": "  "})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if effects, err := InterpretActionEffects(7, effectChanges(test.call)); !errors.Is(err, ErrInvalidActionEffects) || effects != nil {
				t.Fatalf("effects = %#v, error = %v", effects, err)
			}
		})
	}
}

func TestInterpretActionEffectsUsesRuneLengthLimits(t *testing.T) {
	allowed := strings.Repeat("钥", 200)
	if _, err := InterpretActionEffects(7, effectChanges(effectCall("add_item", map[string]any{
		"player_id": 7, "item_name": allowed, "quantity": 1, "description": "可含|分隔符",
	}))); err != nil {
		t.Fatalf("200-rune item name rejected: %v", err)
	}
	if _, err := InterpretActionEffects(7, effectChanges(effectCall("add_item", map[string]any{
		"player_id": 7, "item_name": allowed + "匙", "quantity": 1, "description": "",
	}))); !errors.Is(err, ErrInvalidActionEffects) {
		t.Fatalf("201-rune item name error = %v", err)
	}
}

func TestInterpretActionEffectsIsAllOrNothing(t *testing.T) {
	effects, err := InterpretActionEffects(7, effectChanges(
		effectCall("set_location", map[string]any{"player_id": 7, "location": "书房"}),
		effectCall("set_location", map[string]any{"player_id": 8, "location": "地下室"}),
	))
	if !errors.Is(err, ErrInvalidActionEffects) || effects != nil {
		t.Fatalf("effects = %#v, error = %v", effects, err)
	}
}

func effectChanges(calls ...map[string]any) map[string]any {
	values := make([]any, len(calls))
	for index := range calls {
		values[index] = calls[index]
	}
	return map[string]any{"calls": values}
}

func effectCall(name string, arguments map[string]any) map[string]any {
	return map[string]any{"name": name, "arguments": arguments}
}

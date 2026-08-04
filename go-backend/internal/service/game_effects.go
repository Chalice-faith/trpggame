package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxActionEffectCalls = 8

// ItemMutation 是受控的道具数量变更；正数增加，负数移除。
type ItemMutation struct {
	Name          string `json:"name"`
	QuantityDelta int    `json:"quantity_delta"`
	Description   string `json:"description,omitempty"`
}

// BuffMutation 是受控的 Buff/DeBuff 持续回合设置。
type BuffMutation struct {
	Name     string `json:"name"`
	Duration int    `json:"duration"`
}

// KeyEventMutation 是 Phase 3 通过 MySQL/Outbox 持久记录的关键剧情事件。
type KeyEventMutation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ActionEffects 是 AI 状态调用经服务端重新校验后的规范化结果。
type ActionEffects struct {
	PlayerStateChanges map[string]string  `json:"player_state_changes"`
	Items              []ItemMutation     `json:"items"`
	Buffs              []BuffMutation     `json:"buffs"`
	Events             []KeyEventMutation `json:"events"`
}

type rawActionEffects struct {
	Calls []rawActionEffectCall `json:"calls"`
}

type rawActionEffectCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type playerStatusArguments struct {
	PlayerID uint   `json:"player_id"`
	Field    string `json:"field"`
	Value    int64  `json:"value"`
	Reason   string `json:"reason"`
}

type addItemArguments struct {
	PlayerID    uint   `json:"player_id"`
	ItemName    string `json:"item_name"`
	Quantity    int    `json:"quantity"`
	Description string `json:"description"`
}

type removeItemArguments struct {
	PlayerID uint   `json:"player_id"`
	ItemName string `json:"item_name"`
	Quantity int    `json:"quantity"`
}

type addBuffArguments struct {
	PlayerID uint   `json:"player_id"`
	BuffName string `json:"buff_name"`
	Duration int    `json:"duration"`
}

type setLocationArguments struct {
	PlayerID uint   `json:"player_id"`
	Location string `json:"location"`
}

type triggerEventArguments struct {
	EventName   string `json:"event_name"`
	Description string `json:"description"`
}

var mutablePlayerStatusFields = map[string]struct{}{
	"hp": {}, "mp": {}, "san": {}, "ac": {}, "level": {},
}

// InterpretActionEffects 不信任 AI 返回值，按当前 Function Calling 契约重新校验并规范化。
func InterpretActionEffects(userID uint, statusChanges map[string]any) (*ActionEffects, error) {
	result := &ActionEffects{
		PlayerStateChanges: make(map[string]string),
		Items:              make([]ItemMutation, 0),
		Buffs:              make([]BuffMutation, 0),
		Events:             make([]KeyEventMutation, 0),
	}
	if statusChanges == nil {
		return result, nil
	}
	if userID == 0 {
		return nil, ErrInvalidActionEffects
	}

	var raw rawActionEffects
	if err := decodeStrict(statusChanges, &raw); err != nil ||
		len(raw.Calls) == 0 || len(raw.Calls) > maxActionEffectCalls {
		return nil, fmt.Errorf("%w: malformed calls", ErrInvalidActionEffects)
	}
	for index, call := range raw.Calls {
		if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
			return nil, fmt.Errorf("%w: call %d has no arguments", ErrInvalidActionEffects, index)
		}
		if err := interpretActionEffectCall(userID, call, result); err != nil {
			return nil, fmt.Errorf("%w: call %d: %v", ErrInvalidActionEffects, index, err)
		}
	}
	return result, nil
}

func interpretActionEffectCall(userID uint, call rawActionEffectCall, result *ActionEffects) error {
	switch call.Name {
	case "update_player_status":
		var arguments playerStatusArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		if err := validateTargetPlayer(userID, arguments.PlayerID); err != nil {
			return err
		}
		if _, allowed := mutablePlayerStatusFields[arguments.Field]; !allowed ||
			utf8.RuneCountInString(strings.TrimSpace(arguments.Reason)) > 500 {
			return fmt.Errorf("invalid player status arguments")
		}
		result.PlayerStateChanges[arguments.Field] = strconv.FormatInt(arguments.Value, 10)
	case "set_location":
		var arguments setLocationArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		if err := validateTargetPlayer(userID, arguments.PlayerID); err != nil {
			return err
		}
		location := strings.TrimSpace(arguments.Location)
		if location == "" || utf8.RuneCountInString(location) > 500 {
			return fmt.Errorf("invalid location")
		}
		result.PlayerStateChanges["location"] = location
	case "add_item":
		var arguments addItemArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		if err := validateTargetPlayer(userID, arguments.PlayerID); err != nil {
			return err
		}
		name := strings.TrimSpace(arguments.ItemName)
		description := strings.TrimSpace(arguments.Description)
		if name == "" || strings.Contains(name, "|") ||
			utf8.RuneCountInString(name) > 200 || arguments.Quantity <= 0 ||
			utf8.RuneCountInString(description) > 1000 {
			return fmt.Errorf("invalid add item arguments")
		}
		result.Items = append(result.Items, ItemMutation{
			Name: name, QuantityDelta: arguments.Quantity, Description: description,
		})
	case "remove_item":
		var arguments removeItemArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		if err := validateTargetPlayer(userID, arguments.PlayerID); err != nil {
			return err
		}
		name := strings.TrimSpace(arguments.ItemName)
		if name == "" || strings.Contains(name, "|") ||
			utf8.RuneCountInString(name) > 200 || arguments.Quantity <= 0 {
			return fmt.Errorf("invalid remove item arguments")
		}
		result.Items = append(result.Items, ItemMutation{
			Name: name, QuantityDelta: -arguments.Quantity,
		})
	case "add_buff":
		var arguments addBuffArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		if err := validateTargetPlayer(userID, arguments.PlayerID); err != nil {
			return err
		}
		name := strings.TrimSpace(arguments.BuffName)
		if name == "" || utf8.RuneCountInString(name) > 200 || arguments.Duration <= 0 {
			return fmt.Errorf("invalid buff arguments")
		}
		result.Buffs = append(result.Buffs, BuffMutation{Name: name, Duration: arguments.Duration})
	case "trigger_event":
		var arguments triggerEventArguments
		if err := decodeStrictJSON(call.Arguments, &arguments); err != nil {
			return err
		}
		name := strings.TrimSpace(arguments.EventName)
		description := strings.TrimSpace(arguments.Description)
		if name == "" || utf8.RuneCountInString(name) > 200 || description == "" ||
			utf8.RuneCountInString(description) > 2000 {
			return fmt.Errorf("invalid event arguments")
		}
		result.Events = append(result.Events, KeyEventMutation{Name: name, Description: description})
	default:
		return fmt.Errorf("unsupported effect %q", call.Name)
	}
	return nil
}

func validateTargetPlayer(userID, targetID uint) error {
	if targetID == 0 || targetID != userID {
		return fmt.Errorf("effect targets another player")
	}
	return nil
}

func decodeStrict(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return decodeStrictJSON(encoded, target)
}

func decodeStrictJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

package service

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"trpggame/internal/model"
)

func TestPhase3LegacySaveFixturesKeepStrictDecodersAndOriginalJSON(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			body, err := os.ReadFile("testdata/phase3/legacy-" + version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var save model.GameSave
			if err := json.Unmarshal(body, &save); err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), save.RedisSnapshot...)
			if version == "v1" {
				snapshot, err := decodeGameSaveSnapshot(&save, 41, 7, save.ID)
				if err != nil || snapshot.Turn != 10 || snapshot.Summary != save.SummaryMemory || snapshot.Items[0].Name != "旧钥匙" {
					t.Fatalf("V1 fixture %v %v", snapshot, err)
				}
			} else {
				snapshot, err := decodeMultiplayerSaveSnapshot(&save, 41, save.ID)
				if err != nil || snapshot.CurrentTurn != 15 || len(snapshot.Players) != 3 || snapshot.SummaryMemory != save.SummaryMemory {
					t.Fatalf("V2 fixture %v %v", snapshot, err)
				}
			}
			if !bytes.Equal(original, save.RedisSnapshot) {
				t.Fatal("decoder rewrote legacy JSON")
			}
		})
	}
}

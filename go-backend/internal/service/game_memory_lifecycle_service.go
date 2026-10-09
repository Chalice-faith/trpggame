package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"trpggame/internal/config"
	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type MemoryLifecycleJournal interface {
	FindState(context.Context, uint) (*model.GameMemoryState, error)
	FindOperation(context.Context, uint, string) (*model.GameMemoryOperation, error)
	FindEndOperation(context.Context, uint, string) (*model.GameMemoryOperation, error)
	FindTimeline(context.Context, uint, string) (*model.GameTimeline, error)
	PrepareOperation(context.Context, *model.GameMemoryOperation, *model.GameTimeline) (*model.GameMemoryOperation, error)
	PrepareOperationAtRevision(context.Context, *model.GameMemoryOperation, *model.GameTimeline, uint64) (*model.GameMemoryOperation, error)
	AdvanceOperation(context.Context, uint, string, string, string) error
	CompleteOperationAndProgress(context.Context, uint, string, repo.MemoryOperationProgress) error
	FinalizeOperation(context.Context, uint, string, string) error
	PauseOperationRoom(context.Context, uint, string, uint64) error
	ListRecoverableOperations(context.Context, time.Time, int) ([]model.GameMemoryOperation, error)
	ScheduleOperationRetry(context.Context, uint, string, string, uint, time.Time, string) (bool, error)
	Archive(context.Context, *model.GameActionRecord) (bool, error)
}

type MemoryLifecycleRuntime interface {
	GetGameArchive(context.Context, uint) (*model.GameArchiveRuntime, error)
	GetMemoryControlSource(context.Context, uint, uint, string) (*model.MemoryControlSource, error)
	InitializeMemoryStart(context.Context, model.MemoryRuntimeReplacement) error
	AbortMemoryStart(context.Context, model.MemoryRuntimeReplacement) error
	FenceMemoryOperation(context.Context, model.MemoryRuntimeReplacement) error
	ApplyMemoryOperation(context.Context, model.MemoryRuntimeReplacement) error
	FinishMemoryOperation(context.Context, model.MemoryRuntimeReplacement) error
	InspectMemoryOperation(context.Context, model.MemoryRuntimeReplacement) (*model.MemoryControlProgress, error)
	CaptureMemorySoloRoom(context.Context, uint, uint, model.GameArchiveExpectation) (*model.SoloRuntimeSnapshot, error)
	GetMultiplayerRoom(context.Context, uint) (*model.MultiplayerRuntimeSnapshot, error)
	TransitionMemorySoloRoomStatus(context.Context, uint, uint, []model.RoomStatus, model.RoomStatus, model.GameArchiveExpectation) (bool, error)
	TransitionMemoryMultiplayerRoom(context.Context, uint, model.GameArchiveExpectation, string, model.RoomStatus, model.RoomStatus, time.Time) (int, error)
	ListPendingMemoryAutoSaves(context.Context, uint) ([]model.PendingMemoryAutoSave, error)
	ListPendingMemoryAutoSaveRooms(context.Context, time.Time, int) ([]uint, error)
	AcknowledgeMemoryAutoSave(context.Context, uint, model.PendingMemoryAutoSave) error
}

type MemoryLifecycleGames interface {
	GameRepository
	FindRoomByID(context.Context, uint) (*model.GameRoom, error)
	FindPlayersByRoom(context.Context, uint) ([]model.RoomPlayer, error)
	CreateCurrentMemorySave(context.Context, *model.GameSave, uint64) error
	TransitionMemoryRoomStatus(context.Context, uint, uint, string, uint64, model.RoomStatus) error
}

type memoryRoomStarter interface {
	Start(context.Context, uint, uint, uint64) (*repo.RoomRecord, error)
}

type memoryOperationImage struct {
	LifecycleVersion int             `json:"lifecycle_version"`
	OwnerID          uint            `json:"owner_id"`
	Mode             string          `json:"mode"`
	SourceRevision   uint64          `json:"source_revision"`
	TargetRevision   uint64          `json:"target_revision"`
	Generation       string          `json:"generation"`
	RoomVersion      uint64          `json:"room_version"`
	TimeoutMS        int64           `json:"timeout_ms"`
	Roster           []uint          `json:"roster"`
	Save             *model.GameSave `json:"save,omitempty"`
	Opening          bool            `json:"opening"`
	LegacyBaseline   bool            `json:"legacy_baseline"`
}

type GameMemoryLifecycleService struct {
	journal MemoryLifecycleJournal
	runtime MemoryLifecycleRuntime
	games   MemoryLifecycleGames
	archive *GameArchiveService
	starter memoryRoomStarter
	options config.GameArchiveConfig
}

func NewGameMemoryLifecycleService(journal MemoryLifecycleJournal, runtime MemoryLifecycleRuntime, games MemoryLifecycleGames, archive *GameArchiveService, options config.GameArchiveConfig) (*GameMemoryLifecycleService, error) {
	if journal == nil || runtime == nil || games == nil || archive == nil {
		return nil, errors.New("missing memory lifecycle repositories")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &GameMemoryLifecycleService{journal: journal, runtime: runtime, games: games, archive: archive, options: options}, nil
}

func (s *GameMemoryLifecycleService) ConfigureRoomStarter(starter memoryRoomStarter) {
	s.starter = starter
}

func memoryMode(room *model.GameRoom) string {
	if room.IsSolo {
		return "solo"
	}
	return "multiplayer"
}
func operationUUID(id, purpose string) string {
	parsed, _ := uuid.Parse(id)
	return uuid.NewSHA1(parsed, []byte(purpose)).String()
}

func (s *GameMemoryLifecycleService) State(ctx context.Context, roomID uint) (*model.GameMemoryState, error) {
	state, err := s.journal.FindState(ctx, roomID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		meta, probe := s.runtime.GetGameArchive(ctx, roomID)
		if probe != nil {
			return nil, probe
		}
		if meta != nil {
			return nil, repo.ErrGameArchiveCorrupt
		}
		return nil, nil
	}
	return state, err
}

func (s *GameMemoryLifecycleService) Start(ctx context.Context, room *model.GameRoom, solo *model.SoloRuntimeSnapshot, multi *model.MultiplayerRuntimeSnapshot) (*model.GameMemoryOperation, error) {
	if room == nil || room.Status != model.RoomStatusWaiting {
		return nil, ErrGameStartConflict
	}
	id := uuid.NewString()
	timeline := operationUUID(id, "timeline")
	generation := operationUUID(id, "target-generation")
	save, err := encodeMemorySave(room.ID, "", timeline, 0, true, "opening", solo, multi, false)
	if err != nil {
		return nil, err
	}
	image := memoryOperationImage{LifecycleVersion: 1, OwnerID: room.OwnerID, Mode: memoryMode(room), TargetRevision: 3, Generation: generation, RoomVersion: room.Version, Save: save, Opening: true, Roster: []uint{room.OwnerID}}
	if multi != nil {
		image.Roster = append([]uint(nil), multi.TurnOrder...)
		image.TimeoutMS = int64(room.TurnTimeoutSeconds) * 1000
	}
	body, _ := json.Marshal(image)
	fingerprint, _ := model.MemoryHash(body)
	op := &model.GameMemoryOperation{OperationID: id, RoomID: room.ID, Kind: "start", Fingerprint: fingerprint, SourceGeneration: generation, TargetTimelineID: &timeline, TargetSnapshot: body, NextRetryAt: s.initialRecoveryTime()}
	prepared, err := s.journal.PrepareOperation(ctx, op, &model.GameTimeline{ID: timeline, RoomID: room.ID, HistoryComplete: true})
	if err != nil {
		return nil, err
	}
	err = s.ProcessOperation(ctx, prepared, false)
	return prepared, err
}

func (s *GameMemoryLifecycleService) Load(ctx context.Context, req *LoadGameRequest, room *model.GameRoom, save *model.GameSave) (*LoadGameResult, error) {
	if !model.ValidMemoryUUID(req.RequestID) {
		return nil, ErrInvalidGameLoad
	}
	fingerprint, _ := model.MemoryHash(struct {
		Kind                    string
		RoomID, OwnerID, SaveID uint
	}{"load", room.ID, req.UserID, save.ID})
	if existing, err := s.journal.FindOperation(ctx, room.ID, req.RequestID); err == nil {
		if existing.Kind != "load" || existing.Fingerprint != fingerprint || existing.TargetSaveID == nil || *existing.TargetSaveID != save.ID {
			return nil, repo.ErrMemoryConflict
		}
		if err = s.ProcessOperation(ctx, existing, false); err != nil {
			return nil, err
		}
		_, input, err := s.operationImage(existing)
		if err != nil {
			return nil, err
		}
		turn := 0
		if input.Solo != nil {
			turn = input.Solo.Turn
		} else {
			turn = input.Multiplayer.CurrentTurn
		}
		return loadedGameResult(room.ID, save.ID, turn), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	decoded, err := decodeMemorySave(save, room, req.UserID)
	if err != nil {
		return nil, err
	}
	if err := repo.ValidateSummarySave(save); err != nil {
		return nil, err
	}
	if decoded.Multiplayer != nil {
		if err := validateMultiplayerRoomRoster(ctx, s.games, room, decoded.Multiplayer); err != nil {
			return nil, err
		}
	}
	source, err := s.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil || source == nil {
		if err == nil {
			err = repo.ErrGameArchiveCorrupt
		}
		return nil, err
	}
	if source.Memory.ControlOperationID != "" || source.Memory.ArchiveState == "blocked" {
		return nil, repo.ErrMemoryBusy
	}
	timeline := operationUUID(req.RequestID, "timeline")
	generation := operationUUID(req.RequestID, "target-generation")
	position := uint64(0)
	complete := false
	summarySource := "legacy"
	var parent *string
	if !decoded.Legacy {
		position = *decoded.Envelope.Memory.Position
		complete = *decoded.Envelope.Memory.HistoryComplete
		summarySource = decoded.Envelope.Memory.SummarySource
		parent = save.TimelineID
	}
	targetSave, err := encodeMemorySave(room.ID, "", timeline, position, complete, summarySource, decoded.Solo, decoded.Multiplayer, false)
	if err != nil {
		return nil, err
	}
	image := memoryOperationImage{LifecycleVersion: 1, OwnerID: room.OwnerID, Mode: memoryMode(room), SourceRevision: source.Memory.Revision, TargetRevision: source.Memory.Revision + 3,
		Generation: generation, Save: targetSave, LegacyBaseline: decoded.Legacy, Roster: []uint{room.OwnerID}}
	if !room.IsSolo {
		image.Roster = append([]uint(nil), decoded.Multiplayer.TurnOrder...)
		image.TimeoutMS = int64(room.TurnTimeoutSeconds) * 1000
	}
	body, _ := json.Marshal(image)
	op := &model.GameMemoryOperation{OperationID: req.RequestID, RoomID: room.ID, Kind: "load", Fingerprint: fingerprint, SourceTimelineID: &source.Memory.TimelineID,
		SourceGeneration: source.Generation, TargetTimelineID: &timeline, TargetSaveID: &save.ID, TargetSnapshot: body, NextRetryAt: s.initialRecoveryTime()}
	target := &model.GameTimeline{ID: timeline, RoomID: room.ID, ParentID: parent, ForkPosition: position, DurablePosition: position, OriginSaveID: &save.ID, HistoryComplete: complete}
	prepared, err := s.journal.PrepareOperationAtRevision(ctx, op, target, source.Memory.Revision)
	if err != nil {
		return nil, err
	}
	if err = s.ProcessOperation(ctx, prepared, false); err != nil {
		return nil, err
	}
	turn := targetSave.RoundNumber
	if decoded.Multiplayer != nil {
		turn = decoded.Multiplayer.CurrentTurn
	}
	return loadedGameResult(room.ID, save.ID, turn), nil
}

func (s *GameMemoryLifecycleService) End(ctx context.Context, room *model.GameRoom, state *model.GameMemoryState) (*EndGameResult, error) {
	if state.ActiveTimelineID == nil {
		return nil, repo.ErrGameArchiveCorrupt
	}
	if existing, err := s.journal.FindEndOperation(ctx, room.ID, *state.ActiveTimelineID); err == nil {
		if err = s.ProcessOperation(ctx, existing, false); err != nil {
			return nil, err
		}
		return &EndGameResult{RoomID: room.ID, Status: model.RoomStatusEnded}, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
		return nil, ErrGameRoomNotEndable
	}
	source, err := s.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil || source == nil {
		if err == nil {
			err = repo.ErrGameArchiveCorrupt
		}
		return nil, err
	}
	var roster []uint
	if source.Memory.ControlOperationID != "" || source.Memory.ArchiveState == "blocked" || source.Memory.TimelineID != *state.ActiveTimelineID {
		return nil, repo.ErrMemoryBusy
	}
	if room.IsSolo {
		roster = []uint{room.OwnerID}
	} else {
		if decodeStrictJSON(room.TurnOrder, &roster) != nil {
			return nil, ErrMultiplayerSaveIncompatible
		}
	}
	id := uuid.NewString()
	image := memoryOperationImage{LifecycleVersion: 1, OwnerID: room.OwnerID, Mode: memoryMode(room), SourceRevision: source.Memory.Revision,
		TargetRevision: source.Memory.Revision + 3, Generation: operationUUID(id, "target-generation"), Roster: roster}
	if !room.IsSolo {
		image.TimeoutMS = int64(room.TurnTimeoutSeconds) * 1000
	}
	body, _ := json.Marshal(image)
	fingerprint, _ := model.MemoryHash(struct {
		RoomID   uint
		Timeline string
		Kind     string
	}{room.ID, *state.ActiveTimelineID, "end"})
	op, err := s.journal.PrepareOperationAtRevision(ctx, &model.GameMemoryOperation{OperationID: id, RoomID: room.ID, Kind: "end", Fingerprint: fingerprint,
		SourceTimelineID: state.ActiveTimelineID, SourceGeneration: source.Generation, TargetSnapshot: body, NextRetryAt: s.initialRecoveryTime()}, nil, source.Memory.Revision)
	if err != nil {
		return nil, err
	}
	if err = s.ProcessOperation(ctx, op, false); err != nil {
		return nil, err
	}
	return &EndGameResult{RoomID: room.ID, Status: model.RoomStatusEnded}, nil
}

// Recovery must not race the initial bounded HTTP processing. In particular,
// recovery deliberately pauses a start, whereas a successful fresh start plays.
// No new schema or permanent lock: abandoned intents become due after this grace.
func (s *GameMemoryLifecycleService) initialRecoveryTime() time.Time {
	delay := max(s.options.LeaseMS, 2*s.options.OperationTimeoutMS)
	return time.Now().UTC().Add(time.Duration(delay) * time.Millisecond)
}

func (s *GameMemoryLifecycleService) operationImage(op *model.GameMemoryOperation) (*memoryOperationImage, model.MemoryRuntimeReplacement, error) {
	if op == nil {
		return nil, model.MemoryRuntimeReplacement{}, repo.ErrGameArchiveCorrupt
	}
	copy := *op
	if copy.Seal() != nil {
		return nil, model.MemoryRuntimeReplacement{}, repo.ErrGameArchiveCorrupt
	}
	var image memoryOperationImage
	if decodeStrictJSON(op.TargetSnapshot, &image) != nil || image.LifecycleVersion != 1 || image.OwnerID == 0 || !model.ValidMemoryUUID(image.Generation) ||
		(image.Mode != "solo" && image.Mode != "multiplayer") || image.TargetRevision == 0 || image.TargetRevision > 9007199254740990 {
		return nil, model.MemoryRuntimeReplacement{}, repo.ErrGameArchiveCorrupt
	}
	if (op.Kind == "start" && (image.SourceRevision != 0 || image.TargetRevision != 3 || !image.Opening || image.LegacyBaseline)) ||
		(op.Kind != "start" && (image.SourceRevision == 0 || image.SourceRevision > 9007199254740987 || image.TargetRevision != image.SourceRevision+3 || image.Opening)) ||
		(op.Kind == "end" && (image.Save != nil || image.LegacyBaseline)) || len(image.Roster) == 0 {
		return nil, model.MemoryRuntimeReplacement{}, repo.ErrGameArchiveCorrupt
	}
	input := model.MemoryRuntimeReplacement{RoomID: op.RoomID, OwnerID: image.OwnerID, Mode: image.Mode, Kind: op.Kind, OperationID: op.OperationID, SnapshotHash: op.SnapshotHash,
		SourceGeneration: op.SourceGeneration, SourceRevision: image.SourceRevision, Revision: image.TargetRevision, Generation: image.Generation, TurnTimeoutMS: image.TimeoutMS, Roster: image.Roster}
	if op.SourceTimelineID != nil {
		input.SourceTimeline = *op.SourceTimelineID
	}
	if op.TargetTimelineID != nil {
		input.TimelineID = *op.TargetTimelineID
	}
	if op.Kind != "end" {
		if image.Save == nil || image.Save.RoomID != op.RoomID || image.Save.TimelineID == nil || *image.Save.TimelineID != input.TimelineID || image.Save.MemoryPosition == nil {
			return nil, input, repo.ErrGameArchiveCorrupt
		}
		room := &model.GameRoom{ID: op.RoomID, OwnerID: image.OwnerID, IsSolo: image.Mode == "solo"}
		save := *image.Save
		save.ID = 1
		decoded, err := decodeMemorySave(&save, room, image.OwnerID)
		if err != nil || decoded.Legacy {
			return nil, input, repo.ErrGameArchiveCorrupt
		}
		input.Solo = decoded.Solo
		input.Multiplayer = decoded.Multiplayer
		input.Position = *image.Save.MemoryPosition
		order := []uint{image.OwnerID}
		if input.Multiplayer != nil {
			order = input.Multiplayer.TurnOrder
		}
		if len(order) != len(image.Roster) {
			return nil, input, repo.ErrGameArchiveCorrupt
		}
		for i, user := range order {
			if user != image.Roster[i] {
				return nil, input, repo.ErrGameArchiveCorrupt
			}
		}
	}
	if op.Kind == "start" {
		record, err := memoryBoundaryRecord(op, &image, input, "opening")
		if err != nil {
			return nil, input, err
		}
		input.Opening = record
	}
	return &image, input, nil
}

func memoryBoundaryRecord(op *model.GameMemoryOperation, image *memoryOperationImage, input model.MemoryRuntimeReplacement, kind string) (*model.GameActionRecord, error) {
	if image.Save == nil {
		return nil, repo.ErrGameArchiveCorrupt
	}
	position := uint64(0)
	namespace := "legacy"
	turn, round := 0, 0
	if input.Solo != nil {
		turn = input.Solo.Turn
		round = turn
	} else if input.Multiplayer != nil {
		turn = input.Multiplayer.CurrentTurn
		round = input.Multiplayer.RoundNumber
	}
	if kind == "opening" {
		position = 1
		namespace = "opening"
	}
	payload, _ := json.Marshal(struct {
		Mode     string          `json:"mode"`
		Runtime  json.RawMessage `json:"runtime"`
		Summary  string          `json:"summary"`
		Messages json.RawMessage `json:"messages"`
	}{image.Mode, image.Save.RedisSnapshot, image.Save.SummaryMemory, image.Save.RecentMessages})
	r := &model.GameActionRecord{CommitID: operationUUID(op.OperationID, kind+"-commit"), RoomID: op.RoomID, TimelineID: input.TimelineID, Position: position, Kind: kind,
		RequestNamespace: namespace, RequestID: operationUUID(op.OperationID, kind+"-request"), Fingerprint: op.Fingerprint, SourceGeneration: input.Generation, SourceRevision: input.Revision,
		TurnBefore: turn, TurnAfter: turn, RoundBefore: round, RoundAfter: round, PayloadVersion: 1, Payload: payload}
	return r, r.Seal()
}

// Every phase can be repeated with the same journal/image. Completed operations
// still retry Redis finalization; a receipt prevents an old worker touching a new branch.
func (s *GameMemoryLifecycleService) ProcessOperation(parent context.Context, original *model.GameMemoryOperation, recovery bool) error {
	ctx, cancel := context.WithTimeout(parent, time.Duration(s.options.OperationTimeoutMS)*time.Millisecond)
	defer cancel()
	op, err := s.journal.FindOperation(ctx, original.RoomID, original.OperationID)
	if err != nil {
		return err
	}
	image, input, err := s.operationImage(op)
	if err != nil {
		return err
	}
	input.Recovery = recovery
	if op.Phase == "blocked" || op.Phase == "aborted" {
		return repo.ErrMemoryBusy
	}
	if receipt, probe := s.runtime.InspectMemoryOperation(ctx, input); probe == nil && receipt.Phase == "finished" {
		if op.Phase != "completed" {
			return repo.ErrGameArchiveCorrupt
		}
		if op.Kind == "start" {
			if meta, err := s.runtime.GetGameArchive(ctx, op.RoomID); err == nil && meta != nil && meta.TimelineID == input.TimelineID {
				_, _ = s.archive.process(ctx, op.RoomID)
			}
		}
		return s.journal.FinalizeOperation(ctx, op.RoomID, op.OperationID, op.SnapshotHash)
	}
	room, err := s.games.FindRoomByIDAndOwnerID(ctx, op.RoomID, image.OwnerID)
	if err != nil {
		return err
	}
	if memoryMode(room) != image.Mode {
		return repo.ErrGameArchiveCorrupt
	}
	if op.Phase == "prepared" {
		if op.Kind == "start" {
			if err = s.runtime.InitializeMemoryStart(ctx, input); err != nil {
				return err
			}
			if room.Status == model.RoomStatusWaiting {
				if room.IsSolo {
					_, err = s.games.TransitionRoomStatus(ctx, room.ID, room.OwnerID, []model.RoomStatus{model.RoomStatusWaiting}, model.RoomStatusPlaying)
				} else if s.starter != nil {
					_, err = s.starter.Start(ctx, room.OwnerID, room.ID, image.RoomVersion)
				} else {
					err = ErrMultiplayerRuntimeUnavailable
				}
				if err != nil {
					// A lost database response may have activated the room already.
					confirmed, probe := s.games.FindRoomByIDAndOwnerID(ctx, room.ID, image.OwnerID)
					if probe == nil && (confirmed.Status == model.RoomStatusPlaying || confirmed.Status == model.RoomStatusPaused) {
						room = confirmed
					} else {
						if permanentStartError(err) {
							if cleanup := s.runtime.AbortMemoryStart(ctx, input); cleanup != nil {
								return cleanup
							}
							if abort := s.journal.AdvanceOperation(ctx, op.RoomID, op.OperationID, "prepared", "aborted"); abort != nil {
								return abort
							}
						}
						return err
					}
				}
				room, err = s.games.FindRoomByIDAndOwnerID(ctx, room.ID, image.OwnerID)
				if err != nil {
					return err
				}
			}
			if room.Status != model.RoomStatusPlaying && room.Status != model.RoomStatusPaused {
				return repo.ErrMemoryConflict
			}
		} else {
			if err = s.runtime.FenceMemoryOperation(ctx, input); err != nil {
				return err
			}
			if _, err = s.games.TransitionRoomStatus(ctx, room.ID, image.OwnerID, []model.RoomStatus{model.RoomStatusPlaying, model.RoomStatusPaused}, model.RoomStatusPaused); err != nil {
				return err
			}
			if err = s.Drain(ctx, room.ID); err != nil {
				return err
			}
		}
		if recovery && op.Kind == "start" {
			if err = s.journal.PauseOperationRoom(ctx, op.RoomID, op.OperationID, input.Revision); err != nil {
				return err
			}
		}
		if err = s.runtime.ApplyMemoryOperation(ctx, input); err != nil {
			return err
		}
		if err = s.journal.AdvanceOperation(ctx, op.RoomID, op.OperationID, "prepared", "redis_applied"); err != nil {
			return err
		}
		op, err = s.journal.FindOperation(ctx, op.RoomID, op.OperationID)
		if err != nil {
			return err
		}
	}
	if op.Phase == "redis_applied" {
		if recovery && op.Kind != "end" {
			if op.Kind == "start" {
				if err = s.journal.PauseOperationRoom(ctx, op.RoomID, op.OperationID, input.Revision); err != nil {
					return err
				}
			}
			if err = s.runtime.ApplyMemoryOperation(ctx, input); err != nil {
				return err
			}
		}
		progress, err := s.runtime.InspectMemoryOperation(ctx, input)
		if err != nil {
			return err
		}
		if progress.Phase != "applied" {
			return repo.ErrGameArchiveCorrupt
		}
		turn, round := progress.SourceTurn, progress.SourceRound
		if input.Solo != nil {
			turn = input.Solo.Turn
			round = turn
		} else if input.Multiplayer != nil {
			turn = input.Multiplayer.CurrentTurn
			round = input.Multiplayer.RoundNumber
		}
		if err = s.journal.CompleteOperationAndProgress(ctx, op.RoomID, op.OperationID, repo.MemoryOperationProgress{OwnerID: image.OwnerID, Solo: image.Mode == "solo", Turn: turn, Round: round, SnapshotHash: op.SnapshotHash}); err != nil {
			return err
		}
		op, err = s.journal.FindOperation(ctx, op.RoomID, op.OperationID)
		if err != nil {
			return err
		}
	}
	if op.Phase != "completed" {
		return repo.ErrGameArchiveCorrupt
	}
	if recovery && op.Kind != "end" {
		meta, err := s.runtime.GetGameArchive(ctx, op.RoomID)
		if err != nil {
			return err
		}
		if op.Kind != "start" || (meta != nil && meta.HeadPosition == 0) {
			if op.Kind == "start" {
				if err = s.journal.PauseOperationRoom(ctx, op.RoomID, op.OperationID, input.Revision); err != nil {
					return err
				}
			}
			if err = s.runtime.ApplyMemoryOperation(ctx, input); err != nil {
				return err
			}
		}
	}
	if image.LegacyBaseline {
		// Restore only from the exact persisted image while the token is held.
		baseline, err := memoryBoundaryRecord(op, image, input, "legacy_baseline")
		if err != nil {
			return err
		}
		if _, err = s.journal.Archive(ctx, baseline); err != nil {
			return err
		}
	}
	if err = s.runtime.FinishMemoryOperation(ctx, input); err != nil {
		return err
	}
	if err = s.journal.FinalizeOperation(ctx, op.RoomID, op.OperationID, op.SnapshotHash); err != nil {
		return err
	}
	if op.Kind == "start" {
		_, _ = s.archive.process(ctx, op.RoomID)
	}
	return nil
}

func permanentStartError(err error) bool {
	return errors.Is(err, repo.ErrRoomVersionConflict) || errors.Is(err, repo.ErrRoomStartConditions) || errors.Is(err, repo.ErrRoomPermission) || errors.Is(err, repo.ErrRoomClosed) || errors.Is(err, repo.ErrRoomMissing)
}

func (s *GameMemoryLifecycleService) Drain(ctx context.Context, roomID uint) error {
	memory, err := s.runtime.GetGameArchive(ctx, roomID)
	if err != nil {
		return err
	}
	if memory == nil {
		return repo.ErrGameArchiveCorrupt
	}
	if memory.ArchiveState == "blocked" {
		return repo.ErrGameArchiveNotReady
	}
	if memory.HeadPosition > memory.DurablePosition {
		if _, err = s.archive.process(ctx, roomID); err != nil {
			return err
		}
		memory, err = s.runtime.GetGameArchive(ctx, roomID)
		if err != nil {
			return err
		}
		if memory.HeadPosition != memory.DurablePosition {
			return repo.ErrGameArchiveNotReady
		}
	}
	return s.FlushAutoSaves(ctx, roomID)
}

func (s *GameMemoryLifecycleService) FlushAutoSaves(ctx context.Context, roomID uint) error {
	pending, err := s.runtime.ListPendingMemoryAutoSaves(ctx, roomID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	room, err := s.games.FindRoomByID(ctx, roomID)
	if err != nil {
		return err
	}
	for _, item := range pending {
		timeline, err := s.journal.FindTimeline(ctx, roomID, item.TimelineID)
		if err != nil {
			return err
		}
		if timeline.Status != "active" || item.Position > timeline.DurablePosition {
			return repo.ErrMemoryGap
		}
		save, err := memoryBoundarySave(item, room, timeline.HistoryComplete)
		if err != nil {
			return err
		}
		if _, err = s.games.CreateAutoSave(ctx, save); err != nil {
			return err
		}
		if err = s.runtime.AcknowledgeMemoryAutoSave(ctx, roomID, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *GameMemoryLifecycleService) ManualSave(ctx context.Context, room *model.GameRoom, name string) (*CreateManualSaveResult, error) {
	if err := s.Pause(ctx, room); err != nil {
		return nil, err
	}
	if err := s.Drain(ctx, room.ID); err != nil {
		return nil, err
	}
	source, err := s.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil {
		return nil, err
	}
	if source == nil || source.Memory == nil || source.Status != model.RoomStatusPaused {
		return nil, repo.ErrGameArchiveNotReady
	}
	if source.Memory.ControlOperationID != "" || source.Memory.ArchiveState != "ready" {
		return nil, repo.ErrMemoryBusy
	}
	timeline, err := s.journal.FindTimeline(ctx, room.ID, source.Memory.TimelineID)
	if err != nil {
		return nil, err
	}
	expected := model.GameArchiveExpectation{TimelineID: source.Memory.TimelineID, Generation: source.Generation}
	var solo *model.SoloRuntimeSnapshot
	var multi *model.MultiplayerRuntimeSnapshot
	if room.IsSolo {
		solo, err = s.runtime.CaptureMemorySoloRoom(ctx, room.ID, room.OwnerID, expected)
	} else {
		multi, err = s.runtime.GetMultiplayerRoom(ctx, room.ID)
		if err == nil {
			err = validateMultiplayerRoomRoster(ctx, s.games, room, multi)
		}
	}
	if err != nil {
		return nil, err
	}
	if solo != nil && (solo.Status != model.RoomStatusPaused || solo.Memory == nil || solo.Memory.HeadPosition != source.Memory.DurablePosition || solo.Memory.Revision != source.Memory.Revision) {
		return nil, repo.ErrMemoryBusy
	}
	if multi != nil && (multi.Status != model.RoomStatusPaused || multi.Generation != source.Generation || multi.Memory == nil || multi.Memory.TimelineID != source.Memory.TimelineID || multi.Memory.HeadPosition != source.Memory.DurablePosition || multi.Memory.Revision != source.Memory.Revision) {
		return nil, repo.ErrMemoryBusy
	}
	save, err := encodeMemorySave(room.ID, name, source.Memory.TimelineID, source.Memory.DurablePosition, timeline.HistoryComplete, "runtime", solo, multi, false)
	if err != nil {
		return nil, err
	}
	if err = s.games.CreateCurrentMemorySave(ctx, save, source.Memory.Revision); err != nil {
		return nil, err
	}
	return &CreateManualSaveResult{Save: save}, nil
}

func (s *GameMemoryLifecycleService) Pause(ctx context.Context, room *model.GameRoom) error {
	source, err := s.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil || source == nil {
		if err == nil {
			err = repo.ErrGameArchiveCorrupt
		}
		return err
	}
	if source.Memory.ControlOperationID != "" || source.Memory.ArchiveState == "blocked" {
		return repo.ErrMemoryBusy
	}
	if source.Status == model.RoomStatusPlaying {
		expected := model.GameArchiveExpectation{TimelineID: source.Memory.TimelineID, Generation: source.Generation}
		if room.IsSolo {
			var changed bool
			changed, err = s.runtime.TransitionMemorySoloRoomStatus(ctx, room.ID, room.OwnerID, []model.RoomStatus{model.RoomStatusPlaying}, model.RoomStatusPaused, expected)
			if err == nil && !changed {
				err = repo.ErrMemoryBusy
			}
		} else {
			var changed int
			changed, err = s.runtime.TransitionMemoryMultiplayerRoom(ctx, room.ID, expected, uuid.NewString(), model.RoomStatusPlaying, model.RoomStatusPaused, time.Time{})
			if err == nil && changed <= 0 {
				err = repo.ErrMemoryBusy
			}
		}
		if err != nil {
			return err
		}
	}
	if source.Status != model.RoomStatusPlaying && source.Status != model.RoomStatusPaused {
		return ErrGameRoomNotPausable
	}
	return s.games.TransitionMemoryRoomStatus(ctx, room.ID, room.OwnerID, source.Memory.TimelineID, source.Memory.Revision, model.RoomStatusPaused)
}

func (s *GameMemoryLifecycleService) Resume(ctx context.Context, room *model.GameRoom, state *model.GameMemoryState) error {
	if state.Status != "ready" || state.ActiveOperationID != nil {
		return repo.ErrMemoryBusy
	}
	if err := s.Drain(ctx, room.ID); err != nil {
		return err
	}
	source, err := s.runtime.GetMemoryControlSource(ctx, room.ID, room.OwnerID, memoryMode(room))
	if err != nil || source == nil {
		if err == nil {
			err = repo.ErrGameArchiveCorrupt
		}
		return err
	}
	if source.Memory.ArchiveState != "ready" || source.Memory.ControlOperationID != "" || state.ActiveTimelineID == nil || *state.ActiveTimelineID != source.Memory.TimelineID || state.Revision != source.Memory.Revision {
		return repo.ErrMemoryBusy
	}
	if source.Status == model.RoomStatusPlaying {
		return s.games.TransitionMemoryRoomStatus(ctx, room.ID, room.OwnerID, source.Memory.TimelineID, source.Memory.Revision, model.RoomStatusPlaying)
	}
	if source.Status != model.RoomStatusPaused {
		return ErrGameRoomNotResumable
	}
	if err = s.games.TransitionMemoryRoomStatus(ctx, room.ID, room.OwnerID, source.Memory.TimelineID, source.Memory.Revision, model.RoomStatusPlaying); err != nil {
		return err
	}
	expected := model.GameArchiveExpectation{TimelineID: source.Memory.TimelineID, Generation: source.Generation}
	if room.IsSolo {
		var changed bool
		changed, err = s.runtime.TransitionMemorySoloRoomStatus(ctx, room.ID, room.OwnerID, []model.RoomStatus{model.RoomStatusPaused}, model.RoomStatusPlaying, expected)
		if err == nil && !changed {
			err = repo.ErrMemoryBusy
		}
	} else {
		var changed int
		changed, err = s.runtime.TransitionMemoryMultiplayerRoom(ctx, room.ID, expected, uuid.NewString(), model.RoomStatusPaused, model.RoomStatusPlaying, time.Now().UTC().Add(time.Duration(room.TurnTimeoutSeconds)*time.Second))
		if err == nil && changed <= 0 {
			err = repo.ErrMemoryBusy
		}
	}
	if err != nil {
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		current, probe := s.runtime.GetMemoryControlSource(rollback, room.ID, room.OwnerID, memoryMode(room))
		if probe == nil && current != nil && current.Memory.TimelineID == source.Memory.TimelineID && current.Memory.Revision == source.Memory.Revision && current.Memory.ControlOperationID == "" && current.Status == model.RoomStatusPlaying {
			return nil
		}
		_ = s.games.TransitionMemoryRoomStatus(rollback, room.ID, room.OwnerID, source.Memory.TimelineID, source.Memory.Revision, model.RoomStatusPaused)
	}
	return err
}

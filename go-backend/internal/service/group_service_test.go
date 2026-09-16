package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"trpggame/internal/model"
	"trpggame/internal/repo"
)

type groupRepoStub struct {
	record       repo.GroupRecord
	rows         []repo.GroupRecord
	members      []repo.GroupMemberRecord
	err          error
	beforeID     uint
	limit        int
	actorID      uint
	groupID      uint
	targetID     uint
	targetIDs    []uint
	version      uint64
	role         model.GroupRole
	name         string
	avatarURL    string
	updateName   *string
	updateAvatar *string
}

func (s *groupRepoStub) mutation() (*repo.GroupMutationResult, error) {
	return &repo.GroupMutationResult{Record: s.record, Changed: true}, s.err
}

type groupMutationRecorder struct{ results []*repo.GroupMutationResult }

func (r *groupMutationRecorder) PublishGroupMutation(result *repo.GroupMutationResult) {
	r.results = append(r.results, result)
}
func (s *groupRepoStub) Create(_ context.Context, actorID uint, name, avatar string, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.name, s.avatarURL = actorID, name, avatar
	return s.mutation()
}
func (s *groupRepoStub) List(_ context.Context, actorID, beforeID uint, limit int) ([]repo.GroupRecord, error) {
	s.actorID, s.beforeID, s.limit = actorID, beforeID, limit
	return append([]repo.GroupRecord(nil), s.rows...), s.err
}
func (s *groupRepoStub) Get(_ context.Context, actorID, groupID uint) (*repo.GroupRecord, error) {
	s.actorID, s.groupID = actorID, groupID
	return &s.record, s.err
}
func (s *groupRepoStub) ListMembers(_ context.Context, actorID, groupID, beforeID uint, limit int) ([]repo.GroupMemberRecord, error) {
	s.actorID, s.groupID, s.beforeID, s.limit = actorID, groupID, beforeID, limit
	return append([]repo.GroupMemberRecord(nil), s.members...), s.err
}
func (s *groupRepoStub) Update(_ context.Context, actorID, groupID uint, version uint64, name, avatar *string, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.groupID, s.version, s.updateName, s.updateAvatar = actorID, groupID, version, name, avatar
	return s.mutation()
}
func (s *groupRepoStub) Invite(_ context.Context, actorID, groupID uint, targetIDs []uint, version uint64, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.groupID, s.targetIDs, s.version = actorID, groupID, append([]uint(nil), targetIDs...), version
	return s.mutation()
}
func (s *groupRepoStub) SetRole(_ context.Context, actorID, groupID, targetID uint, role model.GroupRole, version uint64, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.groupID, s.targetID, s.role, s.version = actorID, groupID, targetID, role, version
	return s.mutation()
}
func (s *groupRepoStub) Remove(_ context.Context, actorID, groupID, targetID uint, version uint64, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.groupID, s.targetID, s.version = actorID, groupID, targetID, version
	return s.mutation()
}
func (s *groupRepoStub) Transfer(_ context.Context, actorID, groupID, targetID uint, version uint64, _ time.Time) (*repo.GroupMutationResult, error) {
	s.actorID, s.groupID, s.targetID, s.version = actorID, groupID, targetID, version
	return s.mutation()
}

func TestGroupServiceCreateNormalizesAndReturnsSummary(t *testing.T) {
	now := time.Now().UTC()
	repository := &groupRepoStub{record: repo.GroupRecord{
		Group:          model.Group{ID: 9, Name: "调查局", OwnerID: 7, Version: 1, CreatedAt: now, UpdatedAt: now},
		ConversationID: 41, CurrentUserRole: model.GroupRoleOwner, MemberCount: 1,
	}}
	result, err := NewGroupService(repository).Create(context.Background(), 7, "  调查局  ", "  https://example.test/a.png ")
	if err != nil || result.ID != 9 || result.ConversationID != 41 || result.CurrentUserRole == nil || *result.CurrentUserRole != model.GroupRoleOwner {
		t.Fatalf("Create() = (%#v, %v)", result, err)
	}
	if repository.name != "调查局" || repository.avatarURL != "https://example.test/a.png" {
		t.Fatalf("normalized values = %q, %q", repository.name, repository.avatarURL)
	}
	for _, name := range []string{"", "\n", string(make([]rune, 81))} {
		if _, err := NewGroupService(repository).Create(context.Background(), 7, name, ""); !errors.Is(err, ErrInvalidGroupRequest) {
			t.Fatalf("name %q error = %v", name, err)
		}
	}
}

func TestGroupServicePaginationAndMemberProjection(t *testing.T) {
	now := time.Now().UTC()
	repository := &groupRepoStub{
		rows: []repo.GroupRecord{
			{Group: model.Group{ID: 9}}, {Group: model.Group{ID: 8}},
		},
		members: []repo.GroupMemberRecord{
			{Member: model.GroupMember{ID: 15, Role: model.GroupRoleAdmin, JoinedAt: now}, User: model.User{ID: 8, Username: "alice"}},
			{Member: model.GroupMember{ID: 14, Role: model.GroupRoleMember, JoinedAt: now}, User: model.User{ID: 9, Username: "bob"}},
		},
	}
	svc := NewGroupService(repository)
	page, err := svc.List(context.Background(), 7, "12", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor != "9" || repository.beforeID != 12 || repository.limit != 2 {
		t.Fatalf("List() = (%#v, %v), repo=%#v", page, err, repository)
	}
	members, err := svc.ListMembers(context.Background(), 7, 9, "20", 1)
	if err != nil || len(members.Items) != 1 || members.Items[0].User.ID != 8 || members.NextCursor != "15" {
		t.Fatalf("ListMembers() = (%#v, %v)", members, err)
	}
	if _, err := svc.List(context.Background(), 7, "bad", 20); !errors.Is(err, ErrInvalidGroupRequest) {
		t.Fatalf("invalid cursor = %v", err)
	}
}

func TestGroupServiceMutationValidationAndErrorMapping(t *testing.T) {
	repository := &groupRepoStub{record: repo.GroupRecord{Group: model.Group{ID: 9, Version: 2}}}
	svc := NewGroupService(repository)
	if _, err := svc.Invite(context.Background(), 7, 9, []uint{8, 8, 10}, 2); err != nil {
		t.Fatal(err)
	}
	if len(repository.targetIDs) != 2 || repository.targetIDs[0] != 8 || repository.targetIDs[1] != 10 {
		t.Fatalf("deduplicated targets = %v", repository.targetIDs)
	}
	if _, err := svc.SetRole(context.Background(), 7, 9, 8, model.GroupRoleOwner, 2); !errors.Is(err, ErrInvalidGroupRequest) {
		t.Fatalf("owner role input = %v", err)
	}
	if _, err := svc.Remove(context.Background(), 7, 9, 8, 0); !errors.Is(err, ErrInvalidGroupRequest) {
		t.Fatalf("zero version = %v", err)
	}
	for repositoryError, serviceError := range map[error]error{
		repo.ErrGroupMissing: ErrGroupNotFound, repo.ErrGroupPermissionDenied: ErrGroupPermissionDenied,
		repo.ErrGroupFriendshipRequired: ErrGroupFriendshipRequired, repo.ErrGroupMemberLimit: ErrGroupMemberLimitReached,
		repo.ErrGroupOwnerConflict: ErrGroupOwnerConflict, repo.ErrGroupVersionConflict: ErrGroupVersionConflict,
	} {
		repository.err = repositoryError
		_, err := svc.Get(context.Background(), 7, 9)
		if !errors.Is(err, serviceError) {
			t.Fatalf("repository error %v mapped to %v", repositoryError, err)
		}
	}
}

func TestGroupServicePublishesCommittedMutation(t *testing.T) {
	repository := &groupRepoStub{record: repo.GroupRecord{Group: model.Group{ID: 9, Version: 2}}}
	publisher := &groupMutationRecorder{}
	svc := NewGroupService(repository)
	svc.SetMutationPublisher(publisher)

	if _, err := svc.Update(context.Background(), 7, 9, 1, ptrString("新群名"), nil); err != nil {
		t.Fatal(err)
	}
	if len(publisher.results) != 1 || publisher.results[0].Record.Group.ID != 9 {
		t.Fatalf("published results = %#v", publisher.results)
	}

	repository.err = repo.ErrGroupVersionConflict
	if _, err := svc.Update(context.Background(), 7, 9, 1, ptrString("冲突"), nil); !errors.Is(err, ErrGroupVersionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if len(publisher.results) != 1 {
		t.Fatalf("failed mutation was published: %#v", publisher.results)
	}
}

func ptrString(value string) *string { return &value }

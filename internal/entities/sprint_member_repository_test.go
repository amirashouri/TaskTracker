package entities

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSprintMemberRoundTripFloorsCapacityAndUpdatesOneRow(t *testing.T) {
	database := newTestDB(t)
	repo := NewSprintMemberRepository(database.DB)
	ctx := context.Background()

	member := &SprintMember{
		ID:                   SprintMemberID("sprint-1:user-1"),
		ProjectID:            ProjectID("project-1"),
		SprintID:             SprintID("sprint-1"),
		UserID:               UserID("user-1"),
		AvailableStoryPoints: -5,
	}
	if err := repo.Create(ctx, member); err != nil {
		t.Fatalf("create: %v", err)
	}
	if member.AvailableStoryPoints != 0 {
		t.Fatalf("create capacity = %d, want floored 0", member.AvailableStoryPoints)
	}

	stored, err := repo.GetByID(ctx, member.ID)
	if err != nil || stored == nil {
		t.Fatalf("get after create = %v, %v", stored, err)
	}
	if stored.AvailableStoryPoints != 0 {
		t.Errorf("stored capacity = %d, want 0", stored.AvailableStoryPoints)
	}

	stored.AvailableStoryPoints = 8
	if err := repo.Update(ctx, stored); err != nil {
		t.Fatalf("update: %v", err)
	}
	all, err := repo.BySprint(ctx, SprintID("sprint-1"))
	if err != nil {
		t.Fatalf("by sprint: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("by sprint = %d rows, want one membership", len(all))
	}
	if all[0].AvailableStoryPoints != 8 {
		t.Errorf("updated capacity = %d, want 8", all[0].AvailableStoryPoints)
	}
	if all[0].Version == member.Version {
		t.Error("version did not move on update")
	}
}

func TestSprintMemberDeleteRetiresTheRow(t *testing.T) {
	database := newTestDB(t)
	repo := NewSprintMemberRepository(database.DB)
	ctx := context.Background()

	member := &SprintMember{
		ID:        SprintMemberID("sprint-1:user-2"),
		ProjectID: ProjectID("project-1"),
		SprintID:  SprintID("sprint-1"),
		UserID:    UserID("user-2"),
	}
	if err := repo.Create(ctx, member); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.Delete(ctx, member.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := repo.GetByID(ctx, member.ID)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("get after delete = %+v, want nil", got)
	}
}

func TestSprintMemberDecodesTheClientKeySpelling(t *testing.T) {
	payload := []byte(`{"id":"sprint-1:user-3","projectID":"project-1","sprintID":"sprint-1","userID":"user-3","availableStoryPoints":13,"createdAt":"2026-11-19T10:00:00Z","updatedAt":"2026-11-19T10:00:00Z","version":"v1"}`)
	member, err := SprintMemberFromJSON(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if member.ProjectID != "project-1" || member.SprintID != "sprint-1" || member.UserID != "user-3" {
		t.Fatalf("decoded ids = %+v", member)
	}
	if member.AvailableStoryPoints != 13 {
		t.Errorf("capacity = %d, want 13", member.AvailableStoryPoints)
	}
	encoded, err := json.Marshal(member)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("marshal produced invalid json: %s", encoded)
	}
}

package entities

import (
	"context"
	"errors"
	"testing"
)

// A work item may carry one epic, and the link survives a write and a read. It
// is orthogonal to sprint membership: the item keeps its sprint, if any.
func TestWorkItemEpicIDRoundTrips(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	items := NewWorkItemRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")
	epic := &Epic{ProjectIDs: []ProjectID{frontend.ID, backend.ID}, Title: "Checkout"}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	item := &WorkItem{
		ProjectID: frontend.ID,
		Key:       "FE-1",
		Title:     "Wire the cart",
		Status:    WorkItemStatusTodo,
		Priority:  PriorityMedium,
		EpicID:    &epic.ID,
	}
	if err := items.Create(ctx, item); err != nil {
		t.Fatalf("create item: %v", err)
	}

	stored, err := items.GetByID(ctx, item.ID)
	if err != nil || stored == nil {
		t.Fatalf("get item = %v, %v", stored, err)
	}
	if stored.EpicID == nil || *stored.EpicID != epic.ID {
		t.Fatalf("EpicID = %v, want %s", stored.EpicID, epic.ID)
	}

	stored.EpicID = nil
	if err := items.Update(ctx, stored); err != nil {
		t.Fatalf("unlink item: %v", err)
	}
	unlinked, err := items.GetByID(ctx, item.ID)
	if err != nil || unlinked == nil {
		t.Fatalf("get unlinked item = %v, %v", unlinked, err)
	}
	if unlinked.EpicID != nil {
		t.Errorf("EpicID = %v after unlink, want nil", unlinked.EpicID)
	}
}

// The one rule the join table cannot express: an item may join an epic only if
// the epic spans the item's project. Re-sending cannot fix it, so the repository
// returns the typed error the engine maps to `rejected`.
func TestWorkItemRefusesAnEpicThatDoesNotListItsProject(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	items := NewWorkItemRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")
	epic := &Epic{ProjectIDs: []ProjectID{frontend.ID}, Title: "Frontend only"}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	item := &WorkItem{
		ProjectID: backend.ID,
		Key:       "BE-1",
		Title:     "Wrong project",
		Status:    WorkItemStatusTodo,
		Priority:  PriorityMedium,
		EpicID:    &epic.ID,
	}
	err := items.Create(ctx, item)
	var link *EpicLinkError
	if !errors.As(err, &link) {
		t.Fatalf("create error = %v, want EpicLinkError", err)
	}
	if link.EpicID != epic.ID || link.ProjectID != backend.ID {
		t.Errorf("error names epic %s and project %s, want %s and %s",
			link.EpicID, link.ProjectID, epic.ID, backend.ID)
	}
	if stored, err := items.GetByID(ctx, item.ID); err != nil || stored != nil {
		t.Errorf("the refused item was stored anyway (stored=%v, err=%v)", stored, err)
	}
}

// An item may arrive before its epic. That is a missing parent, not a rule
// violation: the repository lets the foreign key report it so the engine keeps
// the change `retry_later` instead of dead-lettering it.
func TestWorkItemWithAMissingEpicIsNotRefusedPermanently(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	items := NewWorkItemRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	missing := EpicID("00000000-0000-0000-0000-000000000000")
	item := &WorkItem{
		ProjectID: frontend.ID,
		Key:       "FE-1",
		Title:     "Epic not here yet",
		Status:    WorkItemStatusTodo,
		Priority:  PriorityMedium,
		EpicID:    &missing,
	}

	err := items.Create(ctx, item)
	if err == nil {
		t.Fatal("an item naming a missing epic was stored; the foreign key did not run")
	}
	var link *EpicLinkError
	if errors.As(err, &link) {
		t.Errorf("a missing epic was refused as a permanent link violation: %v", err)
	}
}

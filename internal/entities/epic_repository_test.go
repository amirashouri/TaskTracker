package entities

import (
	"context"
	"errors"
	"testing"
)

func createEpicProject(t *testing.T, projects *ProjectRepository, keyPrefix, name string) *Project {
	t.Helper()
	project := &Project{KeyPrefix: keyPrefix, Name: name}
	if err := projects.Create(context.Background(), project); err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	return project
}

func TestEpicRoundTripPreservesProjectOrder(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")

	epic := &Epic{
		// The backend is first and repeated: the first occurrence wins and the
		// order the user chose is what the client reads back.
		ProjectIDs: []ProjectID{backend.ID, frontend.ID, backend.ID},
		Title:      "Checkout",
		Details:    "One feature across both apps",
		DueDate:    timestampPtr(1700000000000),
	}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	stored, err := epics.GetByID(ctx, epic.ID)
	if err != nil || stored == nil {
		t.Fatalf("get epic = %v, %v", stored, err)
	}
	if len(stored.ProjectIDs) != 2 || stored.ProjectIDs[0] != backend.ID || stored.ProjectIDs[1] != frontend.ID {
		t.Errorf("ProjectIDs = %v, want [%s %s] in that order", stored.ProjectIDs, backend.ID, frontend.ID)
	}
	if stored.Title != "Checkout" || stored.Details != "One feature across both apps" {
		t.Errorf("stored epic = %+v, want the authored title and details", stored)
	}
	if stored.DueDate == nil || *stored.DueDate != 1700000000000 {
		t.Errorf("DueDate = %v, want 1700000000000", stored.DueDate)
	}
	if stored.IsArchived {
		t.Error("a new epic was stored archived")
	}
}

func TestEpicUpdateReplacesItsProjectSet(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")
	mobile := createEpicProject(t, projects, "MO", "Mobile")

	epic := &Epic{ProjectIDs: []ProjectID{frontend.ID, backend.ID}, Title: "Checkout"}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	epic.ProjectIDs = []ProjectID{backend.ID, mobile.ID}
	epic.Title = "Checkout v2"
	epic.IsArchived = true
	previousVersion := epic.Version
	if err := epics.Update(ctx, epic); err != nil {
		t.Fatalf("update epic: %v", err)
	}

	stored, err := epics.GetByID(ctx, epic.ID)
	if err != nil || stored == nil {
		t.Fatalf("get epic = %v, %v", stored, err)
	}
	if len(stored.ProjectIDs) != 2 || stored.ProjectIDs[0] != backend.ID || stored.ProjectIDs[1] != mobile.ID {
		t.Errorf("ProjectIDs = %v, want the replacement set [%s %s]", stored.ProjectIDs, backend.ID, mobile.ID)
	}
	if !stored.IsArchived || stored.Title != "Checkout v2" {
		t.Errorf("stored epic = %+v, want the updated title and archived state", stored)
	}

	// A stale write must not silently replace the set the newer write stored.
	stale := *stored
	stale.ProjectIDs = []ProjectID{frontend.ID}
	stale.Version = previousVersion
	if err := epics.Update(ctx, &stale); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale update error = %v, want ErrVersionConflict", err)
	}
	after, err := epics.GetByID(ctx, epic.ID)
	if err != nil || after == nil {
		t.Fatalf("get after stale update = %v, %v", after, err)
	}
	if len(after.ProjectIDs) != 2 {
		t.Errorf("a refused stale update changed the project set to %v", after.ProjectIDs)
	}
}

func TestEpicDeleteRemovesItsProjectLinks(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")
	epic := &Epic{ProjectIDs: []ProjectID{frontend.ID, backend.ID}, Title: "Checkout"}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	if err := epics.Delete(ctx, epic.ID); err != nil {
		t.Fatalf("delete epic: %v", err)
	}
	if stored, err := epics.GetByID(ctx, epic.ID); err != nil || stored != nil {
		t.Errorf("epic survived its delete (stored=%v, err=%v)", stored, err)
	}

	var links int
	if err := database.QueryRow(`SELECT COUNT(*) FROM epic_projects WHERE epic_id = $1`, epic.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("%d project links survived the epic delete, want 0", links)
	}
}

func TestEpicLinkStateSeparatesMissingFromUnlisted(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	epics := NewEpicRepository(database.DB)
	ctx := context.Background()

	frontend := createEpicProject(t, projects, "FE", "Frontend")
	backend := createEpicProject(t, projects, "BE", "Backend")
	epic := &Epic{ProjectIDs: []ProjectID{frontend.ID}, Title: "Checkout"}
	if err := epics.Create(ctx, epic); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	exists, lists, err := epics.LinkState(ctx, EpicID("00000000-0000-0000-0000-000000000000"), frontend.ID)
	if err != nil {
		t.Fatalf("link state for a missing epic: %v", err)
	}
	if exists || lists {
		t.Errorf("missing epic reported exists=%v lists=%v, want false/false", exists, lists)
	}

	exists, lists, err = epics.LinkState(ctx, epic.ID, backend.ID)
	if err != nil {
		t.Fatalf("link state for an unlisted project: %v", err)
	}
	if !exists || lists {
		t.Errorf("unlisted project reported exists=%v lists=%v, want true/false", exists, lists)
	}

	exists, lists, err = epics.LinkState(ctx, epic.ID, frontend.ID)
	if err != nil {
		t.Fatalf("link state for a listed project: %v", err)
	}
	if !exists || !lists {
		t.Errorf("listed project reported exists=%v lists=%v, want true/true", exists, lists)
	}
}

package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func newTestEngine(t *testing.T) (*Engine, *entities.UserRepository, *entities.ProjectRepository, *entities.WorkItemRepository, string) {
	t.Helper()

	database := dbtest.NewDatabase(t)

	users := entities.NewUserRepository(database.DB)
	projects := entities.NewProjectRepository(database.DB)
	workItems := entities.NewWorkItemRepository(database.DB)
	engine := NewEngine(database.DB, projects, workItems,
		entities.NewSprintRepository(database.DB),
		entities.NewCommentRepository(database.DB),
		users)

	user := &entities.User{Email: "sync@example.com", PasswordHash: "secret"}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return engine, users, projects, workItems, string(user.ID)
}

func projectChange(t *testing.T, entryID string, project *entities.Project, operation entities.OutboxOperation, version string) entities.PushChange {
	t.Helper()
	payload, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("marshal project: %v", err)
	}
	return entities.PushChange{
		ID:        entryID,
		Entity:    entities.OutboxEntityProject,
		EntityID:  string(project.ID),
		Operation: operation,
		Payload:   payload,
		Version:   version,
	}
}

func TestPushCreatesAndPullsBack(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := &entities.Project{ID: "11111111-1111-1111-1111-111111111111", KeyPrefix: "TT", Name: "Demo", NextItemNumber: 1}
	results, err := engine.Push(ctx, userID, []entities.PushChange{projectChange(t, "entry-1", project, entities.OutboxOperationUpsert, "")})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusAccepted {
		t.Fatalf("results = %+v, want one accepted change", results)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("project not stored: %v", err)
	}
	if stored.ID != project.ID {
		t.Errorf("stored id = %s, want the pushed id %s", stored.ID, project.ID)
	}

	page, err := engine.Pull(ctx, entities.OutboxEntityProject, "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(page.Changes) != 1 {
		t.Fatalf("pull returned %d changes, want 1", len(page.Changes))
	}
	if page.Changes[0].EntityID != string(project.ID) {
		t.Errorf("pulled entity id = %s, want %s", page.Changes[0].EntityID, project.ID)
	}
	if page.NextCursor == "" {
		t.Error("pull should return a cursor")
	}

	empty, err := engine.Pull(ctx, entities.OutboxEntityProject, page.NextCursor, 0)
	if err != nil {
		t.Fatalf("pull after cursor: %v", err)
	}
	if len(empty.Changes) != 0 {
		t.Errorf("pull after cursor returned %d changes, want 0", len(empty.Changes))
	}
}

func TestPushUpdateRespectsVersion(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := &entities.Project{ID: "22222222-2222-2222-2222-222222222222", KeyPrefix: "TT", Name: "Demo"}
	if res := pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create status = %s (%s)", res.Status, res.Message)
	}

	current, err := projects.GetByID(ctx, project.ID)
	if err != nil || current == nil {
		t.Fatalf("get project: %v", err)
	}

	current.Name = "Renamed"
	ok := pushOne(t, engine, userID, projectChange(t, "e2", current, entities.OutboxOperationUpsert, current.Version))
	if ok.Status != StatusAccepted {
		t.Fatalf("update from the current version = %s (%s), want accepted", ok.Status, ok.Message)
	}

	stale := *current
	stale.Name = "Stale"
	conflict := pushOne(t, engine, userID, projectChange(t, "e3", &stale, entities.OutboxOperationUpsert, current.Version))
	if conflict.Status != StatusConflict {
		t.Fatalf("stale update = %s (%s), want conflict", conflict.Status, conflict.Message)
	}
}

func TestPushDeleteAndUnknownEntity(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := &entities.Project{ID: "33333333-3333-3333-3333-333333333333", KeyPrefix: "TT", Name: "Demo"}
	pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))

	deleted := pushOne(t, engine, userID, projectChange(t, "e2", project, entities.OutboxOperationDelete, ""))
	if deleted.Status != StatusAccepted {
		t.Fatalf("delete = %s (%s), want accepted", deleted.Status, deleted.Message)
	}
	if stored, err := projects.GetByID(ctx, project.ID); err != nil || stored != nil {
		t.Errorf("project should be gone (err=%v)", err)
	}

	unknown := pushOne(t, engine, userID, entities.PushChange{ID: "e3", Entity: "nonsense", Operation: entities.OutboxOperationUpsert})
	if unknown.Status != StatusRejected {
		t.Errorf("unknown entity = %s, want rejected", unknown.Status)
	}
}

func TestPushWorkItem(t *testing.T) {
	engine, _, _, workItems, userID := newTestEngine(t)
	ctx := context.Background()

	project := &entities.Project{ID: "44444444-4444-4444-4444-444444444444", KeyPrefix: "TT", Name: "Demo"}
	pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:          entities.WorkItemID("55555555-5555-5555-5555-555555555555"),
		ProjectID:   project.ID,
		Key:         "TT-1",
		Title:       "Task",
		Status:      entities.WorkItemStatusTodo,
		Priority:    entities.PriorityHigh,
		Recurrence:  &entities.Recurrence{Type: "weekly", Day: 3},
		StoryPoints: intPtr(5),
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}

	result := pushOne(t, engine, userID, entities.PushChange{
		ID:        "e2",
		Entity:    entities.OutboxEntityWorkItem,
		EntityID:  string(item.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   payload,
	})
	if result.Status != StatusAccepted {
		t.Fatalf("work item push = %s (%s), want accepted", result.Status, result.Message)
	}

	stored, err := workItems.GetByID(ctx, item.ID)
	if err != nil || stored == nil {
		t.Fatalf("work item not stored: %v", err)
	}
	if stored.Recurrence == nil || stored.Recurrence.Type != "weekly" {
		t.Errorf("recurrence = %+v, want weekly", stored.Recurrence)
	}
}

func pushOne(t *testing.T, engine *Engine, userID string, change entities.PushChange) entities.PushResult {
	t.Helper()
	results, err := engine.Push(context.Background(), userID, []entities.PushChange{change})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	return results[0]
}

func intPtr(v int) *int { return &v }

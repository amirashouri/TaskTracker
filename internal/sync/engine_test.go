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
	engine := NewEngine(database.DB)

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

// newProject builds a project the pushing user is a member of, which is what
// authorizes its creation.
func newProject(userID, id string) *entities.Project {
	return &entities.Project{
		ID:             entities.ProjectID(id),
		KeyPrefix:      "TT",
		Name:           "Demo",
		MemberIDs:      []entities.UserID{entities.UserID(userID)},
		NextItemNumber: 1,
	}
}

func TestPushCreatesAndPullsBack(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "11111111-1111-1111-1111-111111111111")
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

	page, err := engine.Pull(ctx, userID, entities.OutboxEntityProject, "", 0)
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

	empty, err := engine.Pull(ctx, userID, entities.OutboxEntityProject, page.NextCursor, 0)
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

	project := newProject(userID, "22222222-2222-2222-2222-222222222222")
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

	project := newProject(userID, "33333333-3333-3333-3333-333333333333")
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

	project := newProject(userID, "44444444-4444-4444-4444-444444444444")
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

// A failure after the entity write but before its log row must leave neither:
// the two writes share one transaction. An invalid operation value passes the
// entity write (it is not a delete) and then violates the sync_log CHECK
// constraint, which is the induced failure.
func TestPushRollsBackWhenTheLogRowFails(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "66666666-6666-6666-6666-666666666666")
	change := projectChange(t, "e1", project, entities.OutboxOperation("frobnicate"), "")

	result := pushOne(t, engine, userID, change)
	if result.Status != StatusRetryLater {
		t.Fatalf("status = %s (%s), want retry_later", result.Status, result.Message)
	}

	if stored, err := projects.GetByID(ctx, project.ID); err != nil || stored != nil {
		t.Errorf("the project survived a push whose log row failed (stored=%v, err=%v)", stored, err)
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

func createUser(t *testing.T, users *entities.UserRepository, email string) *entities.User {
	t.Helper()
	user := &entities.User{Email: email, PasswordHash: "secret"}
	if err := users.Create(context.Background(), user); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

// Creating a project the caller is not listed in is refused, so nobody can
// plant a project in another team's feed.
func TestPushRejectsCreatingAProjectTheCallerIsNotIn(t *testing.T) {
	engine, _, _, _, userID := newTestEngine(t)

	project := &entities.Project{ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", KeyPrefix: "TT", Name: "NotMine"}
	result := pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))
	if result.Status != StatusRejected {
		t.Fatalf("status = %s (%s), want rejected", result.Status, result.Message)
	}
}

// A non-member cannot rewrite a project, even by naming themselves in the
// payload: membership is checked against the stored row, not the change.
func TestPushRejectsAProjectTheCallerCannotSee(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "77777777-7777-7777-7777-777777777777")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("owner create = %s (%s)", res.Status, res.Message)
	}

	intruder := createUser(t, users, "intruder@example.com")

	hijack := *project
	hijack.Name = "Hijacked"
	hijack.MemberIDs = []entities.UserID{intruder.ID}
	result := pushOne(t, engine, string(intruder.ID), projectChange(t, "e2", &hijack, entities.OutboxOperationUpsert, ""))
	if result.Status != StatusRejected {
		t.Fatalf("intruder push = %s (%s), want rejected", result.Status, result.Message)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("get project: %v", err)
	}
	if stored.Name != "Demo" {
		t.Errorf("project name = %q, want it unchanged", stored.Name)
	}
}

// A non-member cannot push a work item into a project they cannot see.
func TestPushRejectsAWorkItemInAProjectTheCallerCannotSee(t *testing.T) {
	engine, users, _, workItems, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))

	intruder := createUser(t, users, "intruder@example.com")

	item := &entities.WorkItem{
		ID:        entities.WorkItemID("cccccccc-cccc-cccc-cccc-cccccccccccc"),
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Sneaky",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityLow,
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}

	result := pushOne(t, engine, string(intruder.ID), entities.PushChange{
		ID:        "e2",
		Entity:    entities.OutboxEntityWorkItem,
		EntityID:  string(item.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   payload,
	})
	if result.Status != StatusRejected {
		t.Fatalf("intruder work item = %s (%s), want rejected", result.Status, result.Message)
	}
	if stored, err := workItems.GetByID(ctx, item.ID); err != nil || stored != nil {
		t.Errorf("the work item was stored despite the rejection (stored=%v, err=%v)", stored, err)
	}
}

// The feed is scoped to the caller's memberships.
func TestPullIsScopedToMembership(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "88888888-8888-8888-8888-888888888888")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create = %s (%s)", res.Status, res.Message)
	}
	outsider := createUser(t, users, "outsider@example.com")

	ownerPage, err := engine.Pull(ctx, ownerID, entities.OutboxEntityProject, "", 0)
	if err != nil {
		t.Fatalf("owner pull: %v", err)
	}
	if len(ownerPage.Changes) != 1 {
		t.Fatalf("owner sees %d changes, want 1", len(ownerPage.Changes))
	}

	outsiderPage, err := engine.Pull(ctx, string(outsider.ID), entities.OutboxEntityProject, "", 0)
	if err != nil {
		t.Fatalf("outsider pull: %v", err)
	}
	if len(outsiderPage.Changes) != 0 {
		t.Errorf("outsider sees %d changes, want 0", len(outsiderPage.Changes))
	}
}

// A client that has lost access must still learn a project is gone, so a
// project's own delete row is delivered beyond its membership.
func TestPullDeliversProjectDeletesToEveryone(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "99999999-9999-9999-9999-999999999999")
	pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))
	outsider := createUser(t, users, "outsider@example.com")

	before, err := engine.Pull(ctx, string(outsider.ID), entities.OutboxEntityProject, "", 0)
	if err != nil {
		t.Fatalf("pull before delete: %v", err)
	}
	if len(before.Changes) != 0 {
		t.Fatalf("outsider saw %d changes before the delete, want 0", len(before.Changes))
	}

	if res := pushOne(t, engine, ownerID, projectChange(t, "e2", project, entities.OutboxOperationDelete, "")); res.Status != StatusAccepted {
		t.Fatalf("delete = %s (%s)", res.Status, res.Message)
	}

	after, err := engine.Pull(ctx, string(outsider.ID), entities.OutboxEntityProject, "", 0)
	if err != nil {
		t.Fatalf("pull after delete: %v", err)
	}
	if len(after.Changes) != 1 || after.Changes[0].Operation != entities.OutboxOperationDelete {
		t.Fatalf("outsider changes after delete = %+v, want the one delete", after.Changes)
	}
}

func intPtr(v int) *int { return &v }

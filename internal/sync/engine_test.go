package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func newTestEngine(t *testing.T) (*Engine, *entities.UserRepository, *entities.ProjectRepository, *entities.WorkItemRepository, string) {
	t.Helper()

	database := dbtest.NewDatabase(t)

	users := entities.NewUserRepository(database.DB)
	projects := entities.NewProjectRepository(database.DB)
	workItems := entities.NewWorkItemRepository(database.DB)
	engine := NewEngine(database.DB, "")

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

	page, err := engine.Pull(ctx, userID, ScopeProjects, "", 0)
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

	empty, err := engine.Pull(ctx, userID, ScopeProjects, page.NextCursor, 0)
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

// A payload the schema itself refuses is answered `rejected`, not `retry_later`.
// Both drivers fail the work_items CHECK on a value the column has no room for,
// and no retry can change the value — so the verdict spares the client a retry
// budget it cannot spend, and stops the refused change wedging the push loop.
// The real defect was the same shape: `"priority":"Medium"` against a CHECK that
// allows only lowercase.
func TestPushRejectsAWorkItemTheDatabaseRefuses(t *testing.T) {
	engine, _, _, workItems, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "88888888-8888-8888-8888-888888888888")
	pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:        entities.WorkItemID("99999999-9999-9999-9999-999999999999"),
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Task",
		Status:    entities.WorkItemStatus("nonsense"),
		Priority:  entities.PriorityHigh,
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
	if result.Status != StatusRejected {
		t.Fatalf("status = %s (%s), want rejected", result.Status, result.Message)
	}

	if stored, err := workItems.GetByID(ctx, item.ID); err != nil || stored != nil {
		t.Errorf("the refused work item was stored anyway (stored=%v, err=%v)", stored, err)
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

	ownerPage, err := engine.Pull(ctx, ownerID, ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("owner pull: %v", err)
	}
	if len(ownerPage.Changes) != 1 {
		t.Fatalf("owner sees %d changes, want 1", len(ownerPage.Changes))
	}

	outsiderPage, err := engine.Pull(ctx, string(outsider.ID), ScopeProjects, "", 0)
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

	before, err := engine.Pull(ctx, string(outsider.ID), ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("pull before delete: %v", err)
	}
	if len(before.Changes) != 0 {
		t.Fatalf("outsider saw %d changes before the delete, want 0", len(before.Changes))
	}

	if res := pushOne(t, engine, ownerID, projectChange(t, "e2", project, entities.OutboxOperationDelete, "")); res.Status != StatusAccepted {
		t.Fatalf("delete = %s (%s)", res.Status, res.Message)
	}

	after, err := engine.Pull(ctx, string(outsider.ID), ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("pull after delete: %v", err)
	}
	if len(after.Changes) != 1 || after.Changes[0].Operation != entities.OutboxOperationDelete {
		t.Fatalf("outsider changes after delete = %+v, want the one delete", after.Changes)
	}
}

// The conflict verdict must name the version the server now holds, so the
// client can re-pull instead of guessing.
func TestConflictVerdictCarriesTheServerVersion(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "dddddddd-dddd-dddd-dddd-dddddddddddd")
	if res := pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create = %s (%s)", res.Status, res.Message)
	}
	first, err := projects.GetByID(ctx, project.ID)
	if err != nil || first == nil {
		t.Fatalf("get: %v", err)
	}

	renamed := *first
	renamed.Name = "Renamed"
	if res := pushOne(t, engine, userID, projectChange(t, "e2", &renamed, entities.OutboxOperationUpsert, first.Version)); res.Status != StatusAccepted {
		t.Fatalf("update = %s (%s)", res.Status, res.Message)
	}
	second, err := projects.GetByID(ctx, project.ID)
	if err != nil || second == nil {
		t.Fatalf("get after update: %v", err)
	}

	stale := *first
	stale.Name = "Stale"
	result := pushOne(t, engine, userID, projectChange(t, "e3", &stale, entities.OutboxOperationUpsert, first.Version))
	if result.Status != StatusConflict {
		t.Fatalf("stale push = %s (%s), want conflict", result.Status, result.Message)
	}
	if result.Version != second.Version {
		t.Errorf("conflict verdict version = %q, want the server's %q", result.Version, second.Version)
	}
}

// An update with no envelope version is last-write-wins: the engine has already
// read the row, so the compare-and-swap runs against the stored version rather
// than failing on the payload's empty one.
func TestPushWithoutAVersionOverwrites(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	if res := pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create = %s (%s)", res.Status, res.Message)
	}

	update := *project
	update.Name = "Renamed"
	result := pushOne(t, engine, userID, projectChange(t, "e2", &update, entities.OutboxOperationUpsert, ""))
	if result.Status != StatusAccepted {
		t.Fatalf("versionless update = %s (%s), want accepted", result.Status, result.Message)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", stored.Name)
	}
}

// A cursor that points before the oldest retained row must be told to start
// over, because pruning removed changes it never saw.
func TestPullAsksForAResyncWhenTheCursorIsTooOld(t *testing.T) {
	database := dbtest.NewDatabase(t)
	ctx := context.Background()
	engine := NewEngine(database.DB, "")
	users := entities.NewUserRepository(database.DB)

	user := &entities.User{Email: "retention@example.com", PasswordHash: "secret"}
	if err := users.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := string(user.ID)

	project := newProject(userID, "ffffffff-ffff-ffff-ffff-ffffffffffff")
	if res := pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create = %s (%s)", res.Status, res.Message)
	}

	fresh, err := engine.Pull(ctx, userID, ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if fresh.ResyncRequired {
		t.Error("a client that has seen nothing was told to resync")
	}
	if fresh.NextCursor == "" {
		t.Fatal("pull returned no cursor")
	}

	// Age the only row past the window, then prune it.
	aged := time.Now().Add(-48 * time.Hour).UnixMilli()
	if _, err := database.ExecContext(ctx, `UPDATE sync_log SET created_at = $1`, aged); err != nil {
		t.Fatalf("age row: %v", err)
	}
	pruned, err := engine.Prune(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d rows, want 1", pruned)
	}

	stale, err := engine.Pull(ctx, userID, ScopeProjects, fresh.NextCursor, 0)
	if err != nil {
		t.Fatalf("pull after prune: %v", err)
	}
	if !stale.ResyncRequired {
		t.Error("a cursor older than the retained window was not told to resync")
	}
}

// Pruning must not touch rows inside the window.
func TestPruneKeepsRecentRows(t *testing.T) {
	engine, _, _, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "12121212-1212-1212-1212-121212121212")
	pushOne(t, engine, userID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, ""))

	pruned, err := engine.Prune(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 0 {
		t.Errorf("pruned %d recent rows, want 0", pruned)
	}

	page, err := engine.Pull(ctx, userID, ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(page.Changes) != 1 {
		t.Errorf("pull returned %d changes, want 1", len(page.Changes))
	}
}

func intPtr(v int) *int { return &v }

// clientPayload re-encodes a payload the way the mobile client does: as a JSON
// string holding the document, not as the document itself.
func clientPayload(t *testing.T, raw []byte) []byte {
	t.Helper()
	encoded, err := json.Marshal(string(raw))
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return encoded
}

// A push from the real client has to be accepted. Its payload arrives as a JSON
// string while the engine parses the payload to decide which project a write
// belongs to, so getting this wrong rejects every change the app ever sends —
// and does it as a "rejected" verdict, which the client dead-letters.
func TestPushAcceptsAClientEncodedPayload(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "22222222-2222-2222-2222-222222222222")
	change := projectChange(t, "entry-1", project, entities.OutboxOperationUpsert, "")
	change.Payload = clientPayload(t, change.Payload)

	results, err := engine.Push(ctx, userID, []entities.PushChange{change})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusAccepted {
		t.Fatalf("results = %+v, want one accepted change", results)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("the project was not stored: %v", err)
	}
	if stored.Name != project.Name {
		t.Errorf("name = %q, want %q", stored.Name, project.Name)
	}
}

// The same for a work item, whose project id is read out of the payload before
// the write is authorized.
func TestPushAcceptsAClientEncodedWorkItemPayload(t *testing.T) {
	engine, _, _, workItems, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "33333333-3333-3333-3333-333333333333")
	pushOne(t, engine, userID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	item := &entities.WorkItem{
		ID:        "44444444-4444-4444-4444-444444444444",
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "From the client",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}

	change := entities.PushChange{
		ID:        "w-1",
		Entity:    entities.OutboxEntityWorkItem,
		EntityID:  string(item.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   clientPayload(t, raw),
	}

	results, err := engine.Push(ctx, userID, []entities.PushChange{change})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusAccepted {
		t.Fatalf("results = %+v, want one accepted change", results)
	}

	stored, err := workItems.GetByID(ctx, item.ID)
	if err != nil || stored == nil {
		t.Fatalf("the work item was not stored: %v", err)
	}
	if stored.Title != "From the client" {
		t.Errorf("title = %q, want %q", stored.Title, "From the client")
	}
}

// The client writes timestamps as ISO-8601 UTC strings, because its DateCoding
// fixes the wire format at whole seconds. The server's entity fields were
// epoch millis, so a real push failed to decode with "cannot unmarshal string
// into Go struct field Project.createdAt of type int64" — and because that
// happens while parsing the payload to find the project, every change the app
// ever sent came back rejected.
//
// The payload here is hand-written rather than marshalled, so it is the
// client's spelling and not this server's. That is the whole point: the two
// have to agree, and a test that marshals with the server's own encoder cannot
// tell whether they do.
func TestPushAcceptsAClientPayloadWithISOTimestamps(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "55555555-5555-5555-5555-555555555555")

	raw := []byte(`{
		"id": "` + string(project.ID) + `",
		"keyPrefix": "TT",
		"name": "From the app",
		"details": "",
		"memberIds": ["` + userID + `"],
		"isArchived": false,
		"nextItemNumber": 1,
		"createdAt": "2026-09-26T20:13:32Z",
		"updatedAt": "2026-09-26T20:13:32Z",
		"version": "v-1"
	}`)

	change := entities.PushChange{
		ID:        "iso-1",
		Entity:    entities.OutboxEntityProject,
		EntityID:  string(project.ID),
		Operation: entities.OutboxOperationUpsert,
		Payload:   clientPayload(t, raw),
	}

	results, err := engine.Push(ctx, userID, []entities.PushChange{change})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusAccepted {
		t.Fatalf("results = %+v, want one accepted change", results)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("the project was not stored: %v", err)
	}
	if stored.Name != "From the app" {
		t.Errorf("name = %q, want %q", stored.Name, "From the app")
	}

	want := entities.Timestamp(time.Date(2026, time.September, 26, 20, 13, 32, 0, time.UTC).UnixMilli())
	if stored.CreatedAt != want {
		t.Errorf("createdAt = %s, want %s", stored.CreatedAt, want)
	}
}

// A work item carries the optional timestamps too, and a client that sets a due
// date sends it in the same format as the rest.
func TestPushAcceptsAClientPayloadWithISOTimestampsOnAWorkItem(t *testing.T) {
	engine, _, _, workItems, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "66666666-6666-6666-6666-666666666666")
	pushOne(t, engine, userID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	const itemID = "77777777-7777-7777-7777-777777777777"
	raw := []byte(`{
		"id": "` + itemID + `",
		"projectId": "` + string(project.ID) + `",
		"key": "TT-1",
		"title": "With a due date",
		"details": "",
		"status": "todo",
		"priority": "medium",
		"dueDate": "2026-10-01T09:30:00Z",
		"blockedByIds": [],
		"rank": 0,
		"createdAt": "2026-09-26T20:13:32Z",
		"updatedAt": "2026-09-26T20:13:32Z",
		"version": "v-1"
	}`)

	change := entities.PushChange{
		ID:        "iso-2",
		Entity:    entities.OutboxEntityWorkItem,
		EntityID:  itemID,
		Operation: entities.OutboxOperationUpsert,
		Payload:   clientPayload(t, raw),
	}

	results, err := engine.Push(ctx, userID, []entities.PushChange{change})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(results) != 1 || results[0].Status != StatusAccepted {
		t.Fatalf("results = %+v, want one accepted change", results)
	}

	stored, err := workItems.GetByID(ctx, entities.WorkItemID(itemID))
	if err != nil || stored == nil {
		t.Fatalf("the work item was not stored: %v", err)
	}
	if stored.DueDate == nil {
		t.Fatal("dueDate was dropped")
	}
	want := entities.Timestamp(time.Date(2026, time.October, 1, 9, 30, 0, 0, time.UTC).UnixMilli())
	if *stored.DueDate != want {
		t.Errorf("dueDate = %s, want %s", *stored.DueDate, want)
	}
}

// The server's own stamping has to produce a value the wire can carry, or it
// would write an instant it cannot hand back and the client would see a
// different number than the one that was stored.
func TestStoredTimestampsAreWholeSeconds(t *testing.T) {
	engine, _, projects, _, userID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(userID, "88888888-8888-8888-8888-888888888888")
	pushOne(t, engine, userID, projectChange(t, "p-1", project, entities.OutboxOperationUpsert, ""))

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("the project was not stored: %v", err)
	}
	for name, value := range map[string]entities.Timestamp{
		"createdAt": stored.CreatedAt,
		"updatedAt": stored.UpdatedAt,
	} {
		if value%1000 != 0 {
			t.Errorf("%s = %d, which is not a whole second", name, int64(value))
		}
	}
}

// An admin edits a project server-wide, including projects they are not a
// member of, and the edit still has to reach that project's members. Only the
// change feed does that, so the write must be logged exactly as a push is.
func TestApplyAdminProjectEditsAProjectTheAdminIsNotIn(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	admin := createUser(t, users, "admin@example.com")

	project := newProject(ownerID, "66666666-6666-6666-6666-666666666666")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}
	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload project: %v", err)
	}

	// The admin is not in MemberIDs, so this edit would be refused by a push.
	edited := *stored
	edited.Name = "Renamed by the admin"
	res := engine.ApplyAdminProject(ctx, string(admin.ID), projectChange(t, "e2", &edited, entities.OutboxOperationUpsert, stored.Version))
	if res.Status != StatusAccepted {
		t.Fatalf("admin edit = %s (%s)", res.Status, res.Message)
	}

	after, err := projects.GetByID(ctx, project.ID)
	if err != nil || after == nil {
		t.Fatalf("reload project: %v", err)
	}
	if after.Name != "Renamed by the admin" {
		t.Errorf("stored name = %q, want the admin's edit", after.Name)
	}

	// The member pulls the edit: the row and the log committed together.
	page, err := engine.Pull(ctx, ownerID, ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("member pull: %v", err)
	}
	if len(page.Changes) != 2 {
		t.Fatalf("member sees %d changes, want the create and the edit: %+v", len(page.Changes), page.Changes)
	}
	last := page.Changes[len(page.Changes)-1]
	payload, err := entities.ProjectFromJSON([]byte(last.Payload))
	if err != nil {
		t.Fatalf("decode pulled payload: %v", err)
	}
	if payload.Name != "Renamed by the admin" {
		t.Errorf("the pulled edit names %q, want the admin's edit: the write was not logged", payload.Name)
	}
}

// The admin path skips the membership check; it must not have removed it. A
// push by the same non-member admin is still refused.
func TestPushStillRefusesANonMemberAfterTheAdminPathExists(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	admin := createUser(t, users, "admin@example.com")
	project := newProject(ownerID, "77777777-7777-7777-7777-777777777777")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload project: %v", err)
	}
	edited := *stored
	edited.Name = "Sneaky"

	if res := pushOne(t, engine, string(admin.ID), projectChange(t, "e2", &edited, entities.OutboxOperationUpsert, stored.Version)); res.Status != StatusRejected {
		t.Errorf("a non-member's push = %s, want rejected: the membership check was weakened", res.Status)
	}
}

// A sprint has no feed of its own: it rides `workItems`, which is what the client reads.
// This server used to filter that feed to exactly `entity = 'workItem'`, so a sprint's log
// row was written, committed and then served to nobody — a second device pulled every card
// and no sprint, and the row was behind its cursor before it could ever have learned to
// look. The same applied to comments.
//
// The four assertions are the four things that must stay true together: the sprint and the
// comment are on the workItems feed, a *second member* of the project receives them (the
// reported "another account signed in and the sprint screen was empty"), the projects feed is
// still a projects feed, and membership still governs the sprint rows.
func TestTheWorkItemsScopeCarriesSprintsAndComments(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "c1c1c1c1-c1c1-c1c1-c1c1-c1c1c1c1c1c1")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}

	item := &entities.WorkItem{
		ID:        entities.WorkItemID("c2c2c2c2-c2c2-c2c2-c2c2-c2c2c2c2c2c2"),
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Task",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
	}
	itemPayload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e2", Entity: entities.OutboxEntityWorkItem, EntityID: string(item.ID),
		Operation: entities.OutboxOperationUpsert, Payload: itemPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push work item = %s (%s)", res.Status, res.Message)
	}

	now := entities.Now()
	sprint := &entities.Sprint{
		ID:        entities.SprintID("c3c3c3c3-c3c3-c3c3-c3c3-c3c3c3c3c3c3"),
		ProjectID: project.ID,
		Name:      "Sprint 1",
		StartDate: now,
		EndDate:   now,
	}
	sprintPayload, err := json.Marshal(sprint)
	if err != nil {
		t.Fatalf("marshal sprint: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e3", Entity: entities.OutboxEntitySprint, EntityID: string(sprint.ID),
		Operation: entities.OutboxOperationUpsert, Payload: sprintPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push sprint = %s (%s), want accepted", res.Status, res.Message)
	}

	comment := &entities.Comment{
		ID:         entities.CommentID("c4c4c4c4-c4c4-c4c4-c4c4-c4c4c4c4c4c4"),
		WorkItemID: item.ID,
		AuthorID:   entities.UserID(ownerID),
		Body:       "On it",
		CreatedAt:  now,
	}
	commentPayload, err := json.Marshal(comment)
	if err != nil {
		t.Fatalf("marshal comment: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e4", Entity: entities.OutboxEntityComment, EntityID: string(comment.ID),
		Operation: entities.OutboxOperationUpsert, Payload: commentPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push comment = %s (%s), want accepted", res.Status, res.Message)
	}

	page, err := engine.Pull(ctx, ownerID, ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("pull workItems: %v", err)
	}
	delivered := map[entities.OutboxEntity][]string{}
	for _, change := range page.Changes {
		delivered[change.Entity] = append(delivered[change.Entity], change.EntityID)
	}
	for _, want := range []struct {
		entity entities.OutboxEntity
		id     string
	}{
		{entities.OutboxEntityWorkItem, string(item.ID)},
		{entities.OutboxEntitySprint, string(sprint.ID)},
		{entities.OutboxEntityComment, string(comment.ID)},
	} {
		found := false
		for _, id := range delivered[want.entity] {
			if id == want.id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the %s %s did not come back on the workItems feed, so a client that pulls "+
				"it never learns the row exists (feed carried: %v)", want.entity, want.id, delivered)
		}
	}

	projectsPage, err := engine.Pull(ctx, ownerID, ScopeProjects, "", 0)
	if err != nil {
		t.Fatalf("pull projects: %v", err)
	}
	for _, change := range projectsPage.Changes {
		if change.Entity != entities.OutboxEntityProject {
			t.Errorf("the projects feed carried a %s change: a second copy of every non-project "+
				"row to every client that already pulls workItems", change.Entity)
		}
	}

	// The account that reported the empty sprint screen is a member of someone else's
	// project, not its owner: the sprint rows must reach them too, because the feed is scoped
	// by project membership rather than by who pushed the row.
	member := createUser(t, users, "member@example.com")
	if err := projects.AddMember(ctx, project.ID, member.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	memberPage, err := engine.Pull(ctx, string(member.ID), ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("member pull: %v", err)
	}
	sawSprint := false
	for _, change := range memberPage.Changes {
		if change.Entity == entities.OutboxEntitySprint && change.EntityID == string(sprint.ID) {
			sawSprint = true
		}
	}
	if !sawSprint {
		t.Errorf("a member of the project saw %d workItems changes and no sprint %s: the sprint "+
			"screen on their device stays empty", len(memberPage.Changes), sprint.ID)
	}

	outsider := createUser(t, users, "outsider@example.com")
	outsiderPage, err := engine.Pull(ctx, string(outsider.ID), ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("outsider pull: %v", err)
	}
	if len(outsiderPage.Changes) != 0 {
		t.Errorf("an outsider sees %d changes on the workItems feed, want 0 — widening the scope "+
			"must not widen who it is scoped to", len(outsiderPage.Changes))
	}
}

// Membership is the one entitlement a cursor cannot deliver: a member added today needs
// rows that were logged before their cursor existed.
//
// The reported symptom was exactly this shape — an account signed in, pulled its own
// project up to some cursor, was then added to a project that already had sprints in it,
// and saw an empty board. `id > cursor` cannot reach those rows, so the feed has to say
// "start over" once, and then stop saying it.
func TestAMemberAddedAfterSyncingIsAskedToResyncOnce(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}
	item := &entities.WorkItem{
		ID:        entities.WorkItemID("a2a2a2a2-a2a2-a2a2-a2a2-a2a2a2a2a2a2"),
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Logged before the membership existed",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
	}
	itemPayload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e2", Entity: entities.OutboxEntityWorkItem, EntityID: string(item.ID),
		Operation: entities.OutboxOperationUpsert, Payload: itemPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push work item = %s (%s)", res.Status, res.Message)
	}

	// The device has synced something — its cursor is past these rows, which is the state
	// that used to leave them invisible forever.
	member := createUser(t, users, "late@example.com")
	if err := projects.AddMember(ctx, project.ID, member.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	owed, err := engine.Pull(ctx, string(member.ID), ScopeWorkItems, "1", 0)
	if err != nil {
		t.Fatalf("pull with a cursor: %v", err)
	}
	if !owed.ResyncRequired {
		t.Fatalf("a member added after the feed was read was not asked to resync: the item %s "+
			"and every later change are behind their cursor", item.ID)
	}

	// The replay itself: an empty cursor, which must both deliver the old rows and retire
	// the marker.
	replay, err := engine.Pull(ctx, string(member.ID), ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("replay pull: %v", err)
	}
	if replay.ResyncRequired {
		t.Errorf("the replay was itself answered with resyncRequired, which the engine treats " +
			"as a second ask in one cycle and reports as a failure — it would loop")
	}
	found := false
	for _, change := range replay.Changes {
		if change.Entity == entities.OutboxEntityWorkItem && change.EntityID == string(item.ID) {
			found = true
		}
	}
	if !found {
		t.Errorf("the replay carried %d changes and not the item %s it exists to deliver",
			len(replay.Changes), item.ID)
	}

	// And it stops: the next pull with a cursor is an ordinary one again.
	after, err := engine.Pull(ctx, string(member.ID), ScopeWorkItems, "1", 0)
	if err != nil {
		t.Fatalf("pull after the replay: %v", err)
	}
	if after.ResyncRequired {
		t.Errorf("the grant survived its replay: every cycle from now on would resync this " +
			"device, which is a feed that never catches up")
	}

	// Each feed keeps its own cursor, so clearing one must not clear the others: this
	// member still owes a replay on `projects` and `attachments`.
	projectsOwed, err := engine.Pull(ctx, string(member.ID), ScopeProjects, "1", 0)
	if err != nil {
		t.Fatalf("projects pull: %v", err)
	}
	if !projectsOwed.ResyncRequired {
		t.Errorf("retiring the workItems grant also retired the projects one: that feed's " +
			"cursor is separate, and its history would stay unread")
	}
}

// A member who was already there owes nothing: the set is rewritten on every project save,
// and a grant stamped for a survivor would resync every device each time a project is
// renamed or re-tagged.
func TestReSavingAProjectDoesNotAskExistingMembersToResync(t *testing.T) {
	engine, _, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}

	stored, err := projects.GetByID(ctx, project.ID)
	if err != nil || stored == nil {
		t.Fatalf("read project: %v (%v)", err, stored == nil)
	}
	stored.Name = "Renamed"
	if err := projects.Update(ctx, stored); err != nil {
		t.Fatalf("update project: %v", err)
	}

	page, err := engine.Pull(ctx, ownerID, ScopeProjects, "1", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if page.ResyncRequired {
		t.Errorf("an edit that changed no member asked the project's existing members to " +
			"resync: the grant is stamped for additions only")
	}
}

// A member who is no longer on the project is not owed a replay of it, and must not be
// told to start over for a project they can no longer read.
func TestRemovingAMemberWithdrawsTheOwedReplay(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "b2b2b2b2-b2b2-b2b2-b2b2-b2b2b2b2b2b2")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}

	member := createUser(t, users, "removed@example.com")
	if err := projects.AddMember(ctx, project.ID, member.ID, "member"); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := projects.RemoveMember(ctx, project.ID, member.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}

	// Asserted on `projects` rather than `workItems`: an empty feed answers `resyncRequired`
	// on its own (nothing survived, so the cursor cannot be trusted), which would hide
	// whether the grant itself was withdrawn.
	page, err := engine.Pull(ctx, string(member.ID), ScopeProjects, "1", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if page.ResyncRequired {
		t.Errorf("a removed member was still owed a replay of a project they can no longer " +
			"read, so every cycle would ask them to reread an empty feed")
	}
	if len(page.Changes) != 0 {
		t.Errorf("a removed member sees %d project changes, want 0", len(page.Changes))
	}
}

// The client's scheduling rule is enforced on the push path, not only in each device's
// store. A device that was offline when a sibling sprint was created has no row to find,
// so its own check passes and it pushes a second open window into the same project; the
// server is the only layer that sees both, and the verdict is the only way to tell the
// device. The status must be `rejected` rather than `retry_later`: re-sending the same
// payload can never clear a window another sprint already occupies.
func TestPushRefusesASprintWindowThatOverlapsAnOpenSprint(t *testing.T) {
	engine, _, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "d1d1d1d1-d1d1-d1d1-d1d1-d1d1d1d1d1d1")
	if res := pushOne(t, engine, ownerID, projectChange(t, "e1", project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project = %s (%s)", res.Status, res.Message)
	}

	sprintOne := &entities.Sprint{
		ID:        entities.SprintID("d2d2d2d2-d2d2-d2d2-d2d2-d2d2d2d2d2d2"),
		ProjectID: project.ID,
		Name:      "Sprint 1",
		StartDate: 1000,
		EndDate:   2000,
	}
	onePayload, err := json.Marshal(sprintOne)
	if err != nil {
		t.Fatalf("marshal Sprint 1: %v", err)
	}
	first := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e2", Entity: entities.OutboxEntitySprint, EntityID: string(sprintOne.ID),
		Operation: entities.OutboxOperationUpsert, Payload: onePayload,
	})
	if first.Status != StatusAccepted {
		t.Fatalf("push Sprint 1 = %s (%s), want accepted", first.Status, first.Message)
	}

	// 1500 sits inside 1000–2000 without sharing an endpoint, which is the window a
	// half-remembered "no dates in common" check would let through.
	sprintTwo := &entities.Sprint{
		ID:        entities.SprintID("d3d3d3d3-d3d3-d3d3-d3d3-d3d3d3d3d3d3"),
		ProjectID: project.ID,
		Name:      "Sprint 2",
		StartDate: 1500,
		EndDate:   2500,
	}
	twoPayload, err := json.Marshal(sprintTwo)
	if err != nil {
		t.Fatalf("marshal Sprint 2: %v", err)
	}
	refused := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e3", Entity: entities.OutboxEntitySprint, EntityID: string(sprintTwo.ID),
		Operation: entities.OutboxOperationUpsert, Payload: twoPayload,
	})
	if refused.Status != StatusRejected {
		t.Fatalf("push Sprint 2 over Sprint 1 = %s (%s), want rejected", refused.Status, refused.Message)
	}
	if got, want := refused.Message, "'Sprint 1' overlaps this window. Finish it before starting another."; got != want {
		t.Errorf("refusal message = %q, want %q", got, want)
	}

	// A rejected change is not stored, so the second device pulls only the sprint that
	// actually won the window.
	page, err := engine.Pull(ctx, ownerID, ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("pull workItems: %v", err)
	}
	for _, change := range page.Changes {
		if change.Entity == entities.OutboxEntitySprint && change.EntityID == string(sprintTwo.ID) {
			t.Error("a refused sprint reached the feed, so every device would pull two open windows")
		}
	}

	// History is not a timetable: finishing Sprint 1 frees its window, and the same
	// payload that was refused is then accepted.
	sprintOne.IsFinished = true
	finishedPayload, err := json.Marshal(sprintOne)
	if err != nil {
		t.Fatalf("marshal finished Sprint 1: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e4", Entity: entities.OutboxEntitySprint, EntityID: string(sprintOne.ID),
		Operation: entities.OutboxOperationUpsert, Payload: finishedPayload, Version: first.Version,
	}); res.Status != StatusAccepted {
		t.Fatalf("finish Sprint 1 = %s (%s), want accepted", res.Status, res.Message)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e5", Entity: entities.OutboxEntitySprint, EntityID: string(sprintTwo.ID),
		Operation: entities.OutboxOperationUpsert, Payload: twoPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push Sprint 2 over a finished Sprint 1 = %s (%s), want accepted", res.Status, res.Message)
	}

	sprintThree := &entities.Sprint{
		ID:        entities.SprintID("d4d4d4d4-d4d4-d4d4-d4d4-d4d4d4d4d4d4"),
		ProjectID: project.ID,
		Name:      "Sprint 3",
		StartDate: 3000,
		EndDate:   4000,
	}
	threePayload, err := json.Marshal(sprintThree)
	if err != nil {
		t.Fatalf("marshal Sprint 3: %v", err)
	}
	third := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e6", Entity: entities.OutboxEntitySprint, EntityID: string(sprintThree.ID),
		Operation: entities.OutboxOperationUpsert, Payload: threePayload,
	})
	if third.Status != StatusAccepted {
		t.Fatalf("push adjacent Sprint 3 = %s (%s), want accepted", third.Status, third.Message)
	}

	// Updating a sprint without moving its window must not conflict with itself.
	sprintThree.Name = "Renamed Sprint 3"
	renamedPayload, err := json.Marshal(sprintThree)
	if err != nil {
		t.Fatalf("marshal renamed Sprint 3: %v", err)
	}
	renamed := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e7", Entity: entities.OutboxEntitySprint, EntityID: string(sprintThree.ID),
		Operation: entities.OutboxOperationUpsert, Payload: renamedPayload, Version: third.Version,
	})
	if renamed.Status != StatusAccepted {
		t.Fatalf("update Sprint 3 without moving its window = %s (%s), want accepted", renamed.Status, renamed.Message)
	}

	// The inclusive endpoint at 2000 collides with Sprint 2 and is terminal on
	// the client, with the update-specific sentence. Sprint dates travel at
	// whole-second resolution, so the adjacent start above is 3000.
	sprintThree.StartDate = 2000
	overlappingPayload, err := json.Marshal(sprintThree)
	if err != nil {
		t.Fatalf("marshal overlapping Sprint 3: %v", err)
	}
	updateRefused := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "e8", Entity: entities.OutboxEntitySprint, EntityID: string(sprintThree.ID),
		Operation: entities.OutboxOperationUpsert, Payload: overlappingPayload, Version: renamed.Version,
	})
	if updateRefused.Status != StatusRejected {
		t.Fatalf("update Sprint 3 over Sprint 2 = %s (%s), want rejected", updateRefused.Status, updateRefused.Message)
	}
	if got, want := updateRefused.Message, "'Sprint 2' overlaps this window. Finish it before saving this sprint."; got != want {
		t.Errorf("update refusal message = %q, want %q", got, want)
	}
}

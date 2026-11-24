package sync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

func epicChange(t *testing.T, entryID string, epic *entities.Epic, operation entities.OutboxOperation, version string) entities.PushChange {
	t.Helper()
	payload, err := json.Marshal(epic)
	if err != nil {
		t.Fatalf("marshal epic: %v", err)
	}
	return entities.PushChange{
		ID:        entryID,
		Entity:    entities.OutboxEntityEpic,
		EntityID:  string(epic.ID),
		Operation: operation,
		Payload:   payload,
		Version:   version,
	}
}

func pushProject(t *testing.T, engine *Engine, userID string, project *entities.Project) {
	t.Helper()
	if res := pushOne(t, engine, userID, projectChange(t, "project-"+string(project.ID), project, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create project %s = %s (%s)", project.Name, res.Status, res.Message)
	}
}

func pullEpicIDs(t *testing.T, engine *Engine, userID, cursor string) []string {
	t.Helper()
	page, err := engine.Pull(context.Background(), userID, ScopeEpics, cursor, 0)
	if err != nil {
		t.Fatalf("pull epics: %v", err)
	}
	ids := make([]string, 0, len(page.Changes))
	for _, change := range page.Changes {
		ids = append(ids, change.EntityID)
	}
	return ids
}

// An epic spans two projects, so its one log row must reach the members of both.
// This is the whole reason the visibility join exists: the old single project
// column could only ever name one of them.
func TestPullEpicReachesEveryLinkedProject(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	frontend := newProject(ownerID, "f1f1f1f1-f1f1-f1f1-f1f1-f1f1f1f1f1f1")
	backend := newProject(ownerID, "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1")
	pushProject(t, engine, ownerID, frontend)
	pushProject(t, engine, ownerID, backend)

	backendDev := createUser(t, users, "backend@example.com")
	if err := projects.AddMember(ctx, backend.ID, backendDev.ID, "member"); err != nil {
		t.Fatalf("add backend member: %v", err)
	}
	outsider := createUser(t, users, "outsider@example.com")

	epic := &entities.Epic{
		ID:         entities.EpicID("e1e1e1e1-e1e1-e1e1-e1e1-e1e1e1e1e1e1"),
		ProjectIDs: []entities.ProjectID{frontend.ID, backend.ID},
		Title:      "Checkout",
	}
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-1", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create epic = %s (%s)", res.Status, res.Message)
	}

	for name, userID := range map[string]string{
		"owner of both":    ownerID,
		"backend-only dev": string(backendDev.ID),
	} {
		ids := pullEpicIDs(t, engine, userID, "")
		if len(ids) != 1 || ids[0] != string(epic.ID) {
			t.Errorf("%s pulled %v, want the epic %s", name, ids, epic.ID)
		}
	}

	if ids := pullEpicIDs(t, engine, string(outsider.ID), ""); len(ids) != 0 {
		t.Errorf("an outsider pulled %v, want nothing", ids)
	}
}

// The owner is authored state, not derived: a picker's choice must survive the
// opaque document round-trip and come back on the epic feed unchanged.
func TestEpicOwnerSurvivesSyncUpsert(t *testing.T) {
	engine, users, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "f2f2f2f2-f2f2-f2f2-f2f2-f2f2f2f2f2f2")
	pushProject(t, engine, ownerID, project)
	admin := &entities.User{
		Email:        "epic-owner@example.com",
		PasswordHash: "secret",
		Role:         entities.RoleAdmin,
	}
	if err := users.Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	epic := &entities.Epic{
		ID:         entities.EpicID("e4e4e4e4-e4e4-e4e4-e4e4-e4e4e4e4e4e4"),
		ProjectIDs: []entities.ProjectID{project.ID},
		Title:      "Owned checkout",
		OwnerID:    &admin.ID,
	}
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-owner-1", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create owned epic = %s (%s)", res.Status, res.Message)
	}

	page, err := engine.Pull(ctx, ownerID, ScopeEpics, "", 0)
	if err != nil {
		t.Fatalf("pull epics: %v", err)
	}
	if len(page.Changes) != 1 {
		t.Fatalf("pulled %d epic changes, want 1", len(page.Changes))
	}
	pulled, err := entities.EpicFromJSON([]byte(page.Changes[0].Payload))
	if err != nil {
		t.Fatalf("decode pulled epic: %v", err)
	}
	if pulled.OwnerID == nil || *pulled.OwnerID != admin.ID {
		t.Errorf("pulled owner = %v, want %s", pulled.OwnerID, admin.ID)
	}
}

// The epic has a feed of its own. It must not leak onto the scopes the client
// already pulls, and the work item must not leak onto the epic feed.
func TestEpicScopeIsolation(t *testing.T) {
	engine, _, _, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	project := newProject(ownerID, "a1b2c3d4-a1b2-a1b2-a1b2-a1b2c3d4a1b2")
	pushProject(t, engine, ownerID, project)

	epic := &entities.Epic{
		ID:         entities.EpicID("e2e2e2e2-e2e2-e2e2-e2e2-e2e2e2e2e2e2"),
		ProjectIDs: []entities.ProjectID{project.ID},
		Title:      "Checkout",
	}
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-1", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create epic = %s (%s)", res.Status, res.Message)
	}

	item := &entities.WorkItem{
		ID:        entities.WorkItemID("a3a3a3a3-a3a3-a3a3-a3a3-a3a3a3a3a3a3"),
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
		ID: "item-1", Entity: entities.OutboxEntityWorkItem, EntityID: string(item.ID),
		Operation: entities.OutboxOperationUpsert, Payload: itemPayload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push work item = %s (%s)", res.Status, res.Message)
	}

	for _, scope := range []Scope{ScopeProjects, ScopeWorkItems} {
		page, err := engine.Pull(ctx, ownerID, scope, "", 0)
		if err != nil {
			t.Fatalf("pull %s: %v", scope.name, err)
		}
		for _, change := range page.Changes {
			if change.Entity == entities.OutboxEntityEpic {
				t.Errorf("the %s feed carried the epic %s", scope.name, change.EntityID)
			}
		}
	}

	page, err := engine.Pull(ctx, ownerID, ScopeEpics, "", 0)
	if err != nil {
		t.Fatalf("pull epics: %v", err)
	}
	if len(page.Changes) != 1 || page.Changes[0].Entity != entities.OutboxEntityEpic {
		t.Fatalf("the epics feed carried %+v, want only the epic", page.Changes)
	}
}

// A task may join an epic only when the epic spans the task's project. The
// client's picker enforces the same rule, but an offline device can push a link
// the picker never saw, so the server refuses it for good.
func TestWorkItemEpicRequiresLinkedProject(t *testing.T) {
	engine, _, _, workItems, ownerID := newTestEngine(t)
	ctx := context.Background()

	frontend := newProject(ownerID, "c1c1c1c1-c1c1-c1c1-c1c1-c1c1c1c1c1c1")
	backend := newProject(ownerID, "d1d1d1d1-d1d1-d1d1-d1d1-d1d1d1d1d1d1")
	pushProject(t, engine, ownerID, frontend)
	pushProject(t, engine, ownerID, backend)

	epic := &entities.Epic{
		ID:         entities.EpicID("e3e3e3e3-e3e3-e3e3-e3e3-e3e3e3e3e3e3"),
		ProjectIDs: []entities.ProjectID{frontend.ID},
		Title:      "Frontend only",
	}
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-1", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create epic = %s (%s)", res.Status, res.Message)
	}

	wrong := &entities.WorkItem{
		ID:        entities.WorkItemID("c2c2c2c2-c2c2-c2c2-c2c2-c2c2c2c2c2c2"),
		ProjectID: backend.ID,
		Key:       "BE-1",
		Title:     "Wrong project",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
		EpicID:    &epic.ID,
	}
	wrongPayload, err := json.Marshal(wrong)
	if err != nil {
		t.Fatalf("marshal wrong item: %v", err)
	}
	rejected := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "item-wrong", Entity: entities.OutboxEntityWorkItem, EntityID: string(wrong.ID),
		Operation: entities.OutboxOperationUpsert, Payload: wrongPayload,
	})
	if rejected.Status != StatusRejected {
		t.Fatalf("unlinked item = %s (%s), want rejected", rejected.Status, rejected.Message)
	}
	if !strings.Contains(rejected.Message, string(epic.ID)) {
		t.Errorf("rejection message = %q, want it to name the epic %s", rejected.Message, epic.ID)
	}

	right := &entities.WorkItem{
		ID:        entities.WorkItemID("c3c3c3c3-c3c3-c3c3-c3c3-c3c3c3c3c3c3"),
		ProjectID: frontend.ID,
		Key:       "FE-1",
		Title:     "Right project",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
		EpicID:    &epic.ID,
	}
	rightPayload, err := json.Marshal(right)
	if err != nil {
		t.Fatalf("marshal right item: %v", err)
	}
	accepted := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "item-right", Entity: entities.OutboxEntityWorkItem, EntityID: string(right.ID),
		Operation: entities.OutboxOperationUpsert, Payload: rightPayload,
	})
	if accepted.Status != StatusAccepted {
		t.Fatalf("linked item = %s (%s), want accepted", accepted.Status, accepted.Message)
	}

	stored, err := workItems.GetByID(ctx, right.ID)
	if err != nil || stored == nil {
		t.Fatalf("get linked item = %v, %v", stored, err)
	}
	if stored.EpicID == nil || *stored.EpicID != epic.ID {
		t.Errorf("stored EpicID = %v, want %s", stored.EpicID, epic.ID)
	}
}

// Writing an epic is a write to every project it names, so the caller must be a
// member of all of them. A member of one cannot plant the epic in the other.
func TestEpicPushRequiresEveryProject(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	frontend := newProject(ownerID, "a4a4a4a4-a4a4-a4a4-a4a4-a4a4a4a4a4a4")
	backend := newProject(ownerID, "b4b4b4b4-b4b4-b4b4-b4b4-b4b4b4b4b4b4")
	pushProject(t, engine, ownerID, frontend)
	pushProject(t, engine, ownerID, backend)

	// A member of the frontend only.
	frontendDev := createUser(t, users, "frontend@example.com")
	if err := projects.AddMember(ctx, frontend.ID, frontendDev.ID, "member"); err != nil {
		t.Fatalf("add frontend member: %v", err)
	}

	epic := &entities.Epic{
		ID:         entities.EpicID("e4e4e4e4-e4e4-e4e4-e4e4-e4e4e4e4e4e4"),
		ProjectIDs: []entities.ProjectID{frontend.ID, backend.ID},
		Title:      "Both apps",
	}
	result := pushOne(t, engine, string(frontendDev.ID), epicChange(t, "epic-1", epic, entities.OutboxOperationUpsert, ""))
	if result.Status != StatusRejected {
		t.Fatalf("a member of one project created an epic over two = %s (%s), want rejected", result.Status, result.Message)
	}

	// The owner of both may create it, and the frontend-only member may not
	// delete it: a delete is a write to every project it names too.
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-2", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("owner create = %s (%s)", res.Status, res.Message)
	}
	deleted := pushOne(t, engine, string(frontendDev.ID), epicChange(t, "epic-3", epic, entities.OutboxOperationDelete, ""))
	if deleted.Status != StatusRejected {
		t.Errorf("a member of one project deleted an epic over two = %s (%s), want rejected", deleted.Status, deleted.Message)
	}
}

// An epic's visibility follows its current project set. Dropping a project must
// stop its members receiving later changes, without rewriting what they already
// pulled.
func TestEpicUpdateFollowsItsCurrentProjectSet(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	frontend := newProject(ownerID, "a5a5a5a5-a5a5-a5a5-a5a5-a5a5a5a5a5a5")
	backend := newProject(ownerID, "b5b5b5b5-b5b5-b5b5-b5b5-b5b5b5b5b5b5")
	pushProject(t, engine, ownerID, frontend)
	pushProject(t, engine, ownerID, backend)

	frontendDev := createUser(t, users, "frontend@example.com")
	if err := projects.AddMember(ctx, frontend.ID, frontendDev.ID, "member"); err != nil {
		t.Fatalf("add frontend member: %v", err)
	}
	backendDev := createUser(t, users, "backend@example.com")
	if err := projects.AddMember(ctx, backend.ID, backendDev.ID, "member"); err != nil {
		t.Fatalf("add backend member: %v", err)
	}

	epic := &entities.Epic{
		ID:         entities.EpicID("e5e5e5e5-e5e5-e5e5-e5e5-e5e5e5e5e5e5"),
		ProjectIDs: []entities.ProjectID{frontend.ID, backend.ID},
		Title:      "Both apps",
	}
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-1", epic, entities.OutboxOperationUpsert, "")); res.Status != StatusAccepted {
		t.Fatalf("create epic = %s (%s)", res.Status, res.Message)
	}

	first, err := engine.Pull(ctx, string(frontendDev.ID), ScopeEpics, "", 0)
	if err != nil {
		t.Fatalf("frontend pull: %v", err)
	}
	if len(first.Changes) != 1 {
		t.Fatalf("frontend member saw %d changes, want the create", len(first.Changes))
	}

	stored, err := entities.NewEpicRepository(engine.db).GetByID(ctx, epic.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload epic = %v, %v", stored, err)
	}
	stored.ProjectIDs = []entities.ProjectID{backend.ID}
	stored.Title = "Backend only"
	if res := pushOne(t, engine, ownerID, epicChange(t, "epic-2", stored, entities.OutboxOperationUpsert, stored.Version)); res.Status != StatusAccepted {
		t.Fatalf("update epic = %s (%s)", res.Status, res.Message)
	}

	after, err := engine.Pull(ctx, string(frontendDev.ID), ScopeEpics, first.NextCursor, 0)
	if err != nil {
		t.Fatalf("frontend pull after update: %v", err)
	}
	if len(after.Changes) != 0 {
		t.Errorf("a project dropped from the epic still received its update: %+v", after.Changes)
	}

	backendPage, err := engine.Pull(ctx, string(backendDev.ID), ScopeEpics, "", 0)
	if err != nil {
		t.Fatalf("backend pull: %v", err)
	}
	if len(backendPage.Changes) != 2 {
		t.Errorf("backend member saw %d changes, want the create and the update", len(backendPage.Changes))
	}
}

// The visibility join replaced the single project column. Nothing about an
// ordinary entity's membership result may change: a work item in A still
// reaches A's members and nobody else.
func TestPullMembershipUnchanged(t *testing.T) {
	engine, users, projects, _, ownerID := newTestEngine(t)
	ctx := context.Background()

	projectA := newProject(ownerID, "a6a6a6a6-a6a6-a6a6-a6a6-a6a6a6a6a6a6")
	projectB := newProject(ownerID, "b6b6b6b6-b6b6-b6b6-b6b6-b6b6b6b6b6b6")
	pushProject(t, engine, ownerID, projectA)
	pushProject(t, engine, ownerID, projectB)

	memberB := createUser(t, users, "b-member@example.com")
	if err := projects.AddMember(ctx, projectB.ID, memberB.ID, "member"); err != nil {
		t.Fatalf("add B member: %v", err)
	}

	item := &entities.WorkItem{
		ID:        entities.WorkItemID("a7a7a7a7-a7a7-a7a7-a7a7-a7a7a7a7a7a7"),
		ProjectID: projectA.ID,
		Key:       "A-1",
		Title:     "Only A can see this",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	if res := pushOne(t, engine, ownerID, entities.PushChange{
		ID: "item-1", Entity: entities.OutboxEntityWorkItem, EntityID: string(item.ID),
		Operation: entities.OutboxOperationUpsert, Payload: payload,
	}); res.Status != StatusAccepted {
		t.Fatalf("push item = %s (%s)", res.Status, res.Message)
	}

	page, err := engine.Pull(ctx, string(memberB.ID), ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("pull B: %v", err)
	}
	if len(page.Changes) != 0 {
		t.Errorf("a member of B saw %d workItems changes, want 0", len(page.Changes))
	}

	ownerPage, err := engine.Pull(ctx, ownerID, ScopeWorkItems, "", 0)
	if err != nil {
		t.Fatalf("pull owner: %v", err)
	}
	found := false
	for _, change := range ownerPage.Changes {
		if change.Entity == entities.OutboxEntityWorkItem && change.EntityID == string(item.ID) {
			found = true
		}
	}
	if !found {
		t.Error("the item's own project's member did not receive it")
	}
}

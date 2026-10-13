package entities

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/db"
)

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.New("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }

func mustCreateUser(t *testing.T, repo *UserRepository, email string) *User {
	t.Helper()
	user := &User{Email: email, PasswordHash: "secret"}
	if err := repo.Create(context.Background(), user); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

func containsID(ids []UserID, want UserID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestUserRoundTripPreservesMillis(t *testing.T) {
	database := newTestDB(t)
	repo := NewUserRepository(database.DB)
	ctx := context.Background()

	user := &User{Email: "a@example.com", DisplayName: "A", PasswordHash: "secret", CreatedAt: 1700000000123}
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("create: %v", err)
	}
	if user.CreatedAt != 1700000000123 {
		t.Errorf("CreatedAt = %d, want the caller's value preserved", user.CreatedAt)
	}

	got, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CreatedAt != 1700000000123 {
		t.Errorf("CreatedAt round trip = %d, want 1700000000123", got.CreatedAt)
	}

	// Verifying a password updates last_seen; the read must not fail on it.
	verified, err := repo.VerifyPassword(ctx, "a@example.com", "secret")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if verified.LastSeen == nil {
		t.Error("VerifyPassword should record last_seen")
	}
}

func TestProjectRoundTripMembersAndClientID(t *testing.T) {
	database := newTestDB(t)
	users := NewUserRepository(database.DB)
	projects := NewProjectRepository(database.DB)
	ctx := context.Background()

	owner := mustCreateUser(t, users, "owner@example.com")
	member := mustCreateUser(t, users, "member@example.com")

	const clientID = ProjectID("11111111-1111-1111-1111-111111111111")
	project := &Project{
		ID:             clientID,
		KeyPrefix:      "TT",
		Name:           "Demo",
		MemberIDs:      []UserID{owner.ID},
		NextItemNumber: 1,
	}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create: %v", err)
	}
	if project.ID != clientID {
		t.Errorf("Create replaced the client id: got %s", project.ID)
	}

	got, err := projects.GetByID(ctx, clientID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("project not found")
	}
	if len(got.MemberIDs) != 1 || !containsID(got.MemberIDs, owner.ID) {
		t.Errorf("MemberIDs = %v, want just the owner %s", got.MemberIDs, owner.ID)
	}

	got.Name = "Renamed"
	got.MemberIDs = []UserID{owner.ID, member.ID}
	if err := projects.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded, err := projects.GetByID(ctx, clientID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Name != "Renamed" {
		t.Errorf("Name = %q, want Renamed", reloaded.Name)
	}
	if len(reloaded.MemberIDs) != 2 {
		t.Errorf("MemberIDs = %v, want two members", reloaded.MemberIDs)
	}
}

func TestProjectStaleUpdateConflicts(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create: %v", err)
	}

	stale := *project // the version the caller read
	project.Name = "First"
	if err := projects.Update(ctx, project); err != nil {
		t.Fatalf("update from the current version should succeed: %v", err)
	}

	stale.Name = "Second"
	err := projects.Update(ctx, &stale)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
	}
}

func TestDeleteProjectCascadesToWorkItems(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	workItems := NewWorkItemRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	item := &WorkItem{
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Task",
		Status:    WorkItemStatusTodo,
		Priority:  PriorityMedium,
	}
	if err := workItems.Create(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	if err := projects.Delete(ctx, project.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	got, err := workItems.GetByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	if got != nil {
		t.Error("work item survived its project; ON DELETE CASCADE is not working")
	}
}

func TestWorkItemRoundTrip(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	workItems := NewWorkItemRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	item := &WorkItem{
		ProjectID:    project.ID,
		Key:          "TT-1",
		Title:        "Recurring task",
		Status:       WorkItemStatusTodo,
		Priority:     PriorityHigh,
		DueDate:      int64Ptr(1700000000000),
		StoryPoints:  intPtr(5),
		Recurrence:   &Recurrence{Type: "weekly", Day: 2, Until: int64Ptr(1800000000000)},
		BlockedByIDs: []WorkItemID{"22222222-2222-2222-2222-222222222222"},
	}
	if err := workItems.Create(ctx, item); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := workItems.GetByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Recurrence == nil || got.Recurrence.Type != "weekly" || got.Recurrence.Day != 2 {
		t.Errorf("Recurrence = %+v, want the weekly recurrence", got.Recurrence)
	}
	if got.DueDate == nil || *got.DueDate != 1700000000000 {
		t.Errorf("DueDate = %v, want 1700000000000", got.DueDate)
	}
	if got.StoryPoints == nil || *got.StoryPoints != 5 {
		t.Errorf("StoryPoints = %v, want 5", got.StoryPoints)
	}
	if len(got.BlockedByIDs) != 1 {
		t.Errorf("BlockedByIDs = %v, want one entry", got.BlockedByIDs)
	}

	stale := *got
	got.Title = "Changed"
	if err := workItems.Update(ctx, got); err != nil {
		t.Fatalf("update from the current version: %v", err)
	}
	stale.Title = "Stale"
	if err := workItems.Update(ctx, &stale); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale update error = %v, want ErrVersionConflict", err)
	}
}

func TestSprintAndCommentStaleUpdatesConflict(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	sprints := NewSprintRepository(database.DB)
	workItems := NewWorkItemRepository(database.DB)
	comments := NewCommentRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sprint := &Sprint{ProjectID: project.ID, Name: "Sprint 1", StartDate: 1000, EndDate: 2000}
	if err := sprints.Create(ctx, sprint); err != nil {
		t.Fatalf("create sprint: %v", err)
	}
	staleSprint := *sprint
	sprint.Name = "Sprint 1 renamed"
	if err := sprints.Update(ctx, sprint); err != nil {
		t.Fatalf("update sprint: %v", err)
	}
	if err := sprints.Update(ctx, &staleSprint); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale sprint update error = %v, want ErrVersionConflict", err)
	}

	item := &WorkItem{ProjectID: project.ID, Key: "TT-1", Title: "Task", Status: WorkItemStatusTodo, Priority: PriorityLow}
	if err := workItems.Create(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	comment := &Comment{WorkItemID: item.ID, AuthorID: mustCreateUser(t, NewUserRepository(database.DB), "author@example.com").ID, Body: "first"}
	if err := comments.Create(ctx, comment); err != nil {
		t.Fatalf("create comment: %v", err)
	}
	staleComment := *comment
	comment.Body = "edited"
	if err := comments.Update(ctx, comment); err != nil {
		t.Fatalf("update comment: %v", err)
	}
	if err := comments.Update(ctx, &staleComment); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale comment update error = %v, want ErrVersionConflict", err)
	}
}

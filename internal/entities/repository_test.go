package entities

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/db"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
)

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.NewDatabase(t)
}

func intPtr(v int) *int { return &v }

func timestampPtr(v int64) *Timestamp {
	t := Timestamp(v)
	return &t
}

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

// A deleted account is a separate bucket, and it is the *only* status that
// leaves the live list. An inactive or blank status is still a present user:
// this story adds the delete bucket, it does not reclassify what already existed.
func TestListSplitsDeletedFromEveryOtherStatus(t *testing.T) {
	database := newTestDB(t)
	repo := NewUserRepository(database.DB)
	ctx := context.Background()

	fresh := mustCreateUser(t, repo, "fresh@example.com")
	if fresh.Status != StatusActive {
		t.Errorf("a created user's status = %q, want %q", fresh.Status, StatusActive)
	}

	inactive := mustCreateUser(t, repo, "inactive@example.com")
	if err := repo.SetStatus(ctx, inactive.ID, StatusInactive); err != nil {
		t.Fatalf("set inactive: %v", err)
	}
	blank := mustCreateUser(t, repo, "blank@example.com")
	if err := repo.SetStatus(ctx, blank.ID, ""); err != nil {
		t.Fatalf("set blank: %v", err)
	}
	removed := mustCreateUser(t, repo, "removed@example.com")
	if err := repo.SetStatus(ctx, removed.ID, StatusDeleted); err != nil {
		t.Fatalf("set deleted: %v", err)
	}

	live, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := 3; len(live) != want {
		t.Errorf("List returned %d users (%v), want the %d that are not deleted",
			len(live), emailsOf(live), want)
	}
	if containsEmail(live, "removed@example.com") {
		t.Error("List returned a deleted user; `deleted` is not excluded from the live list")
	}

	gone, err := repo.ListDeleted(ctx)
	if err != nil {
		t.Fatalf("list deleted: %v", err)
	}
	if len(gone) != 1 || gone[0].ID != removed.ID {
		t.Errorf("ListDeleted = %v, want just removed@example.com", emailsOf(gone))
	}
}

func emailsOf(users []*User) []string {
	emails := make([]string, 0, len(users))
	for _, u := range users {
		emails = append(emails, u.Email)
	}
	return emails
}

func containsEmail(users []*User, email string) bool {
	for _, u := range users {
		if u.Email == email {
			return true
		}
	}
	return false
}

// CountByRole is what stops the last admin from being deleted, so a deleted
// admin must not count.
func TestCountByRoleIgnoresDeletedAccounts(t *testing.T) {
	database := newTestDB(t)
	repo := NewUserRepository(database.DB)
	ctx := context.Background()

	first := &User{Email: "first@example.com", PasswordHash: "x", Role: RoleAdmin}
	second := &User{Email: "second@example.com", PasswordHash: "x", Role: RoleAdmin}
	member := &User{Email: "member@example.com", PasswordHash: "x", Role: RoleMember}
	for _, u := range []*User{first, second, member} {
		if err := repo.Create(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.Email, err)
		}
	}

	if n, err := repo.CountByRole(ctx, RoleAdmin); err != nil || n != 2 {
		t.Fatalf("CountByRole(admin) = %d, %v, want 2", n, err)
	}

	if err := repo.SetStatus(ctx, second.ID, StatusDeleted); err != nil {
		t.Fatalf("delete admin: %v", err)
	}
	if n, err := repo.CountByRole(ctx, RoleAdmin); err != nil || n != 1 {
		t.Errorf("CountByRole(admin) = %d, %v, want 1: a deleted admin must not count", n, err)
	}
	if n, err := repo.CountByRole(ctx, RoleMember); err != nil || n != 1 {
		t.Errorf("CountByRole(member) = %d, %v, want 1", n, err)
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
		ID:        clientID,
		KeyPrefix: "TT",
		Name:      "Demo",
		Tags: []ProjectTag{
			{ID: "33333333-3333-3333-3333-333333333333", Name: "Bug", Color: "red", Icon: "bug"},
			{ID: "44444444-4444-4444-4444-444444444444", Name: "Mobile", Color: "blue", Icon: "mobile"},
		},
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
	if len(got.Tags) != 2 || got.Tags[0].Name != "Bug" || got.Tags[1].Icon != "mobile" {
		t.Errorf("Tags = %+v, want the two tags in order", got.Tags)
	}

	got.Name = "Renamed"
	got.Tags = []ProjectTag{
		{ID: "33333333-3333-3333-3333-333333333333", Name: "Defect", Color: "amber", Icon: "bug"},
	}
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
	if len(reloaded.Tags) != 1 || reloaded.Tags[0].Name != "Defect" {
		t.Errorf("Tags = %+v, want the replaced catalog", reloaded.Tags)
	}
	if len(reloaded.MemberIDs) != 2 {
		t.Errorf("MemberIDs = %v, want two members", reloaded.MemberIDs)
	}
}

func TestProjectWithoutTagsScansAnEmptySlice(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := projects.GetByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// A nil slice marshals as `null`, which the client's non-null list cannot
	// decode; an empty slice is the wire contract.
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("Tags = %#v, want an empty non-nil slice", got.Tags)
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
		DueDate:      timestampPtr(1700000000000),
		StoryPoints:  intPtr(5),
		Recurrence:   &Recurrence{Type: "weekly", Day: 2, Until: timestampPtr(1800000000000)},
		BlockedByIDs: []WorkItemID{"22222222-2222-2222-2222-222222222222"},
		TagIDs:       []TagID{"33333333-3333-3333-3333-333333333333"},
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
	if len(got.TagIDs) != 1 || got.TagIDs[0] != TagID("33333333-3333-3333-3333-333333333333") {
		t.Errorf("TagIDs = %v, want one tag", got.TagIDs)
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

func TestSprintLockPersists(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	sprints := NewSprintRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sprint := &Sprint{ProjectID: project.ID, Name: "Sprint 1", StartDate: 1000, EndDate: 2000}
	if err := sprints.Create(ctx, sprint); err != nil {
		t.Fatalf("create sprint: %v", err)
	}

	got, err := sprints.GetByID(ctx, sprint.ID)
	if err != nil {
		t.Fatalf("read sprint: %v", err)
	}
	if got.IsLocked {
		t.Fatal("a new sprint came back locked")
	}

	got.IsLocked = true
	if err := sprints.Update(ctx, got); err != nil {
		t.Fatalf("lock sprint: %v", err)
	}

	locked, err := sprints.GetByID(ctx, sprint.ID)
	if err != nil {
		t.Fatalf("read locked sprint: %v", err)
	}
	if !locked.IsLocked {
		t.Fatal("the lock was not stored")
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

// A sprint window is a schedule, not a label: two unfinished windows in one
// project may not share an instant. The store cannot see a sprint another device
// created while it was offline, so the repository is the only layer that can
// refuse the second one when it is pushed.
func TestSprintWindowsMayNotOverlapWhileBothAreOpen(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	sprints := NewSprintRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo"}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	first := &Sprint{ProjectID: project.ID, Name: "Sprint 1", StartDate: 1000, EndDate: 2000}
	if err := sprints.Create(ctx, first); err != nil {
		t.Fatalf("create first sprint: %v", err)
	}

	// 2001 is the first instant after 2000, the staggered cadence the rule allows.
	second := &Sprint{ProjectID: project.ID, Name: "Sprint 2", StartDate: 2001, EndDate: 3000}
	if err := sprints.Create(ctx, second); err != nil {
		t.Fatalf("create the adjacent sprint: %v", err)
	}

	// Both endpoints are inclusive, so a window starting where another ends is a
	// collision — the same answer the client's `sprintBlockingOverlap` gives.
	refused := &Sprint{ProjectID: project.ID, Name: "Sprint 3", StartDate: 1500, EndDate: 1900}
	err := sprints.Create(ctx, refused)
	var overlap *SprintWindowOverlapError
	if !errors.As(err, &overlap) {
		t.Fatalf("create over an open window = %v, want SprintWindowOverlapError", err)
	}
	if overlap.Blocker != "Sprint 1" {
		t.Errorf("blocker = %q, want the open sprint's name %q", overlap.Blocker, "Sprint 1")
	}
	if got, want := err.Error(), "'Sprint 1' overlaps this window. Finish it before starting another."; got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
	if stored, _ := sprints.GetByID(ctx, refused.ID); stored != nil {
		t.Error("a refused sprint was stored")
	}

	// An edit onto an open sibling is refused with the edit's own tail.
	second.StartDate, second.EndDate = 1900, 2500
	err = sprints.Update(ctx, second)
	if !errors.As(err, &overlap) {
		t.Fatalf("move an open sprint onto another = %v, want SprintWindowOverlapError", err)
	}
	if got, want := err.Error(), "'Sprint 1' overlaps this window. Finish it before saving this sprint."; got != want {
		t.Errorf("update refusal = %q, want %q", got, want)
	}

	// An edit that leaves the window clear of its siblings is not a conflict with
	// itself: the row being updated is excluded from the search.
	second.StartDate, second.EndDate = 3001, 4000
	if err := sprints.Update(ctx, second); err != nil {
		t.Fatalf("update without moving the window: %v", err)
	}

	// History is not a timetable: finishing the first sprint frees its window.
	first.IsFinished = true
	if err := sprints.Update(ctx, first); err != nil {
		t.Fatalf("finish the first sprint: %v", err)
	}
	finished := &Sprint{
		ProjectID: project.ID, Name: "Finished Sprint", StartDate: 3001, EndDate: 4000, IsFinished: true,
	}
	if err := sprints.Create(ctx, finished); err != nil {
		t.Errorf("create a finished sprint over an open sprint = %v, want accepted", err)
	}
	finished.Name = "Renamed Finished Sprint"
	if err := sprints.Update(ctx, finished); err != nil {
		t.Errorf("update a finished sprint over an open sprint = %v, want accepted", err)
	}
	if err := sprints.Create(ctx, &Sprint{ProjectID: project.ID, Name: "Sprint 4", StartDate: 1000, EndDate: 2000}); err != nil {
		t.Errorf("create over a finished sprint = %v, want accepted", err)
	}
}

// Numbers are decimal, so the tenth key is TT-10 rather than the TT-: that
// `string(rune(n+'0'))` produced.
func TestAllocateKeyCountsInDecimal(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo", NextItemNumber: 1}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create: %v", err)
	}

	for want := 1; want <= 11; want++ {
		got, err := projects.AllocateKey(ctx, project.ID)
		if err != nil {
			t.Fatalf("allocate %d: %v", want, err)
		}
		if wantKey := "TT-" + strconv.Itoa(want); got != wantKey {
			t.Fatalf("key %d = %q, want %q", want, got, wantKey)
		}
	}
}

// Two concurrent allocations must not be handed the same number.
func TestAllocateKeyIsAtomic(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)
	ctx := context.Background()

	project := &Project{KeyPrefix: "TT", Name: "Demo", NextItemNumber: 1}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create: %v", err)
	}

	const callers = 20
	keys := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = projects.AllocateKey(ctx, project.ID)
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, callers)
	for i, key := range keys {
		if errs[i] != nil {
			t.Fatalf("allocate %d: %v", i, errs[i])
		}
		if seen[key] {
			t.Errorf("key %q was handed out twice", key)
		}
		seen[key] = true
	}
}

func TestAllocateKeyRejectsAnUnknownProject(t *testing.T) {
	database := newTestDB(t)
	projects := NewProjectRepository(database.DB)

	if _, err := projects.AllocateKey(context.Background(), "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("allocating for a missing project should fail")
	}
}

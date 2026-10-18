package attachments

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

// The file API is small but it is the only place where a request body becomes a
// file on disk, so every refusal matters as much as the happy path: an upload
// that stores bytes before checking membership is a file the wrong person wrote,
// and one that stores them before the metadata row exists is a file nothing will
// ever reference.

const testMaxBytes = 25 << 20

type fixture struct {
	db           *sql.DB
	store        *BlobStore
	mux          *http.ServeMux
	attachmentID string
	memberID     string
	outsiderID   string
	projectID    string
	workItemID   string
}

// newFixture builds a project with one work item, one member and one outsider.
//
// The rows go in through the repositories rather than through the sync engine:
// these tests are about the handler, and a push would make every failure here
// ambiguous between the engine and the handler.
func newFixture(t *testing.T, maxBytes int64) *fixture {
	t.Helper()
	database := dbtest.NewDatabase(t)
	ctx := context.Background()

	users := entities.NewUserRepository(database.DB)
	member := &entities.User{Email: "member@example.com", PasswordHash: "x"}
	outsider := &entities.User{Email: "outsider@example.com", PasswordHash: "x"}
	for _, user := range []*entities.User{member, outsider} {
		if err := users.Create(ctx, user); err != nil {
			t.Fatalf("create user %s: %v", user.Email, err)
		}
	}

	projects := entities.NewProjectRepository(database.DB)
	project := &entities.Project{
		KeyPrefix:      "TT",
		Name:           "Demo",
		NextItemNumber: 1,
		// Only the member is in the project. The outsider is a real user with no
		// membership, which is the case the handler must answer 404 to.
		MemberIDs: []entities.UserID{member.ID},
	}
	if err := projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	items := entities.NewWorkItemRepository(database.DB)
	item := &entities.WorkItem{
		ProjectID: project.ID,
		Key:       "TT-1",
		Title:     "Crash on launch",
		Status:    entities.WorkItemStatusTodo,
		Priority:  entities.PriorityMedium,
		Rank:      1,
	}
	if err := items.Create(ctx, item); err != nil {
		t.Fatalf("create work item: %v", err)
	}

	root := t.TempDir()
	store := &BlobStore{Root: root}
	handler := NewHandler(sync.NewEngine(database.DB, root), database.DB, store, maxBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/projects/{projectId}/work-items/{workItemId}/attachments/{attachmentId}/blob", handler.Upload)
	mux.HandleFunc("GET /v1/projects/{projectId}/work-items/{workItemId}/attachments/{attachmentId}/blob", handler.Download)

	return &fixture{
		db:         database.DB,
		store:      store,
		mux:        mux,
		memberID:   string(member.ID),
		outsiderID: string(outsider.ID),
		projectID:  string(project.ID),
		workItemID: string(item.ID),
	}
}

// addAttachment inserts the metadata row the upload requires, and returns its id.
func (f *fixture) addAttachment(t *testing.T, blobRef string) string {
	t.Helper()
	attachment := &entities.Attachment{
		WorkItemID: entities.WorkItemID(f.workItemID),
		ProjectID:  entities.ProjectID(f.projectID),
		FileName:   "report.txt",
		MimeType:   "text/plain",
		SizeBytes:  3,
		BlobRef:    blobRef,
		CreatedBy:  entities.UserID(f.memberID),
	}
	if err := entities.NewAttachmentRepository(f.db).Create(context.Background(), attachment); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	return string(attachment.ID)
}

func (f *fixture) blobPath(id string) string {
	return filepath.Join(f.store.Root, id+".blob")
}

func (f *fixture) do(t *testing.T, method, userID, workItemID, attachmentID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/v1/projects/%s/work-items/%s/attachments/%s/blob",
		f.projectID, workItemID, attachmentID)
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request = request.WithContext(auth.WithUser(request.Context(), userID, "member"))
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	return recorder
}

func (f *fixture) upload(t *testing.T, userID, attachmentID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, http.MethodPost, userID, f.workItemID, attachmentID, body)
}

func (f *fixture) download(t *testing.T, userID, attachmentID string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, http.MethodGet, userID, f.workItemID, attachmentID, nil)
}

func TestUploadStoresTheBytesAndLinksTheRow(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")

	recorder := f.upload(t, f.memberID, id, []byte("abc"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	stored, err := os.ReadFile(f.blobPath(id))
	if err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	if string(stored) != "abc" {
		t.Errorf("stored bytes = %q, want %q", stored, "abc")
	}

	row, err := entities.NewAttachmentRepository(f.db).GetByID(context.Background(), entities.AttachmentID(id))
	if err != nil || row == nil {
		t.Fatalf("re-read attachment: %v (row %v)", err, row)
	}
	if row.BlobRef != id {
		t.Errorf("blobRef = %q, want %q — the upload must link the row to its bytes", row.BlobRef, id)
	}
}

func TestUploadRefusesABodyOverTheLimit(t *testing.T) {
	// A limit of 4 bytes, so the refusal is about the cap rather than about the
	// default being small enough to trip over by accident.
	f := newFixture(t, 4)
	id := f.addAttachment(t, "")

	recorder := f.upload(t, f.memberID, id, []byte("far too many bytes"))

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(f.blobPath(id)); !os.IsNotExist(err) {
		t.Errorf("an oversize upload must leave no file behind (stat err = %v)", err)
	}
	row, err := entities.NewAttachmentRepository(f.db).GetByID(context.Background(), entities.AttachmentID(id))
	if err != nil || row == nil {
		t.Fatalf("re-read attachment: %v", err)
	}
	if row.BlobRef != "" {
		t.Errorf("blobRef = %q, want empty — a refused upload must not claim the bytes landed", row.BlobRef)
	}
}

func TestUploadRequiresTheMetadataRow(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	// The client uploads before its metadata push has landed. This is the 404 the
	// client reads as "not yet on the server" and retries, so it must be a 404 and
	// not a 400 or a 500.
	missing := string(entities.NewAttachmentID())

	recorder := f.upload(t, f.memberID, missing, []byte("abc"))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(f.blobPath(missing)); !os.IsNotExist(err) {
		t.Errorf("bytes were stored for a row that does not exist (stat err = %v)", err)
	}
}

func TestUploadRejectsANonMember(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")

	recorder := f.upload(t, f.outsiderID, id, []byte("abc"))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a non-member must not learn the file exists; body = %s",
			recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(f.blobPath(id)); !os.IsNotExist(err) {
		t.Errorf("a non-member's upload reached the disk (stat err = %v)", err)
	}
}

func TestDownloadReturnsTheStoredBytesAndType(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")
	if recorder := f.upload(t, f.memberID, id, []byte("abc")); recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", recorder.Code)
	}

	recorder := f.download(t, f.memberID, id)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != "abc" {
		t.Errorf("body = %q, want %q", body, "abc")
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want the stored mime type %q", got, "text/plain")
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(disposition, "report.txt") {
		t.Errorf("Content-Disposition = %q, want it to name the file", disposition)
	}
}

func TestDownloadBeforeUploadIs404(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")

	recorder := f.download(t, f.memberID, id)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — there are no bytes yet; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestDownloadRejectsANonMember(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")
	if recorder := f.upload(t, f.memberID, id, []byte("abc")); recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", recorder.Code)
	}

	recorder := f.download(t, f.outsiderID, id)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "abc") {
		t.Error("a non-member was served the bytes")
	}
}

// A member of the project still cannot reach a file through the wrong work item:
// the path names the pair, and both have to agree with the row.
func TestDownloadRejectsAMismatchedWorkItem(t *testing.T) {
	f := newFixture(t, testMaxBytes)
	id := f.addAttachment(t, "")
	if recorder := f.upload(t, f.memberID, id, []byte("abc")); recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", recorder.Code)
	}

	recorder := f.do(t, http.MethodGet, f.memberID, string(entities.NewWorkItemID()), id, nil)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}

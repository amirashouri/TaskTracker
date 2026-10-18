package entities

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/google/uuid"
)

// ErrVersionConflict is returned by a repository Update when the row has changed
// since the caller read it.
var ErrVersionConflict = errors.New("version conflict")

// ============ Core Types ============

type UserID string
type ProjectID string
type WorkItemID string
type SprintID string
type CommentID string
type AttachmentID string

func NewUserID() UserID             { return UserID(uuid.New().String()) }
func NewProjectID() ProjectID       { return ProjectID(uuid.New().String()) }
func NewWorkItemID() WorkItemID     { return WorkItemID(uuid.New().String()) }
func NewSprintID() SprintID         { return SprintID(uuid.New().String()) }
func NewCommentID() CommentID       { return CommentID(uuid.New().String()) }
func NewAttachmentID() AttachmentID { return AttachmentID(uuid.New().String()) }

// ============ User ============

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// User status values. The column is free text, so these constants are the only
// place the vocabulary is written down.
//
// Deleted is a soft delete: the row is kept so sync_log and project_members
// keep their referential integrity, but the account can no longer sign in and
// its refresh tokens are revoked. It is deliberately distinct from a bare
// status string so "the admin removed this person" can never be confused with
// any other label the column might carry.
const (
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusDeleted  = "deleted"
)

type User struct {
	ID           UserID `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName,omitempty"`
	PasswordHash string `json:"-"`
	Position     string `json:"position,omitempty"`
	AvatarID     string `json:"avatarId,omitempty"`
	Status       string `json:"status,omitempty"`
	Role         string `json:"role,omitempty"`
	// User timestamps stay plain epoch millis. A user is never synced, and its
	// only wire shape is the admin API's snake_case userResponse, which is a
	// separate contract from the sync envelope's ISO-8601.
	LastSeen  *int64 `json:"lastSeen,omitempty"` // epoch millis
	CreatedAt int64  `json:"createdAt"`
}

func (u *User) PreferredGreeting() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	// Extract from email
	for i, c := range u.Email {
		if c == '@' {
			return u.Email[:i]
		}
	}
	return u.Email
}

// ============ Project ============

type Project struct {
	ID             ProjectID `json:"id"`
	KeyPrefix      string    `json:"keyPrefix"`
	Name           string    `json:"name"`
	Details        string    `json:"details"`
	MemberIDs      []UserID  `json:"memberIds"`
	IsArchived     bool      `json:"isArchived"`
	NextItemNumber int       `json:"nextItemNumber"`
	CreatedAt      Timestamp `json:"createdAt"`
	UpdatedAt      Timestamp `json:"updatedAt"`
	Version        string    `json:"version"`
}

// Key formats a work-item key. Numbers are decimal, so item 10 is "TT-10"
// rather than the "TT-:" that `string(rune(n+'0'))` produced. Allocation is
// the repository's job (AllocateKey), not the model's: a counter read and
// incremented in memory cannot be atomic.
func (p *Project) Key(number int) string {
	return p.KeyPrefix + "-" + strconv.Itoa(number)
}

// ============ WorkItem ============

type WorkItemStatus string

const (
	WorkItemStatusBacklog    WorkItemStatus = "backlog"
	WorkItemStatusTodo       WorkItemStatus = "todo"
	WorkItemStatusInProgress WorkItemStatus = "in_progress"
	WorkItemStatusInReview   WorkItemStatus = "in_review"
	WorkItemStatusDone       WorkItemStatus = "done"
	WorkItemStatusClosed     WorkItemStatus = "closed"
)

type Priority string

const (
	PriorityLowest   Priority = "lowest"
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

type Recurrence struct {
	Type  string     `json:"type"` // daily, weekly, monthly, yearly
	Day   int        `json:"day,omitempty"`
	Until *Timestamp `json:"until,omitempty"`
}

type WorkItem struct {
	ID           WorkItemID     `json:"id"`
	ProjectID    ProjectID      `json:"projectId"`
	Key          string         `json:"key"`
	Title        string         `json:"title"`
	Details      string         `json:"details"`
	Status       WorkItemStatus `json:"status"`
	Priority     Priority       `json:"priority"`
	AssigneeID   *UserID        `json:"assigneeId,omitempty"`
	DueDate      *Timestamp     `json:"dueDate,omitempty"`
	Recurrence   *Recurrence    `json:"recurrence,omitempty"`
	StoryPoints  *int           `json:"storyPoints,omitempty"`
	BlockedByIDs []WorkItemID   `json:"blockedByIds"`
	SprintID     *SprintID      `json:"sprintId,omitempty"`
	Rank         int            `json:"rank"`
	CreatedAt    Timestamp      `json:"createdAt"`
	UpdatedAt    Timestamp      `json:"updatedAt"`
	ResolvedAt   *Timestamp     `json:"resolvedAt,omitempty"`
	Version      string         `json:"version"` // optimistic lock
}

func (w *WorkItem) IsTerminal() bool {
	return w.Status == WorkItemStatusDone || w.Status == WorkItemStatusClosed
}

func (w *WorkItem) IsOverdue(now Timestamp) bool {
	if w.IsTerminal() || w.DueDate == nil {
		return false
	}
	return *w.DueDate < now
}

// ============ Sprint ============

type SprintStatus string

const (
	SprintStatusScheduled  SprintStatus = "scheduled"
	SprintStatusInProgress SprintStatus = "in_progress"
	SprintStatusFinished   SprintStatus = "finished"
	SprintStatusArchived   SprintStatus = "archived"
)

type Sprint struct {
	ID              SprintID   `json:"id"`
	ProjectID       ProjectID  `json:"projectId"`
	Name            string     `json:"name"`
	GoalStoryPoints int        `json:"goalStoryPoints"`
	Goal            string     `json:"goal,omitempty"`
	StartDate       Timestamp  `json:"startDate"`
	EndDate         Timestamp  `json:"endDate"`
	OwnerID         *UserID    `json:"ownerId,omitempty"`
	IsFinished      bool       `json:"isFinished"`
	FinishedAt      *Timestamp `json:"finishedAt,omitempty"`
	IsArchived      bool       `json:"isArchived"`
	CreatedAt       Timestamp  `json:"createdAt"`
	UpdatedAt       Timestamp  `json:"updatedAt"`
	Version         string     `json:"version"`
}

func (s *Sprint) StatusAt(now Timestamp) SprintStatus {
	if s.IsArchived {
		return SprintStatusArchived
	}
	if s.IsFinished || now > s.EndDate {
		return SprintStatusFinished
	}
	if now >= s.StartDate {
		return SprintStatusInProgress
	}
	return SprintStatusScheduled
}

func (s *Sprint) Overlaps(other *Sprint) bool {
	return s.StartDate <= other.EndDate && other.StartDate <= s.EndDate
}

// ============ Comment ============

type Comment struct {
	ID         CommentID  `json:"id"`
	WorkItemID WorkItemID `json:"workItemId"`
	AuthorID   UserID     `json:"authorId"`
	Body       string     `json:"body"`
	CreatedAt  Timestamp  `json:"createdAt"`
	ParentID   *CommentID `json:"parentId,omitempty"`
	Version    string     `json:"version"`
}

// Attachment is the synced index entry for one file on a work item. The bytes
// are NOT here: they live in the blob store (AttachmentRoot on disk) and are
// linked by BlobRef once uploaded. The row carries ProjectID denormalised so
// the change feed and the file API can scope by membership without a join.
type Attachment struct {
	ID         AttachmentID `json:"id"`
	WorkItemID WorkItemID   `json:"workItemId"`
	ProjectID  ProjectID    `json:"projectId"`
	FileName   string       `json:"fileName"`
	MimeType   string       `json:"mimeType,omitempty"`
	SizeBytes  int64        `json:"sizeBytes"`
	BlobRef    string       `json:"blobRef,omitempty"`
	CreatedBy  UserID       `json:"createdBy"`
	CreatedAt  Timestamp    `json:"createdAt"`
	Version    string       `json:"version"`
}

// ============ Sync Types ============

type OutboxEntity string

const (
	OutboxEntityProject    OutboxEntity = "project"
	OutboxEntityWorkItem   OutboxEntity = "workItem"
	OutboxEntitySprint     OutboxEntity = "sprint"
	OutboxEntityComment    OutboxEntity = "comment"
	OutboxEntityAttachment OutboxEntity = "attachment"
)

type OutboxOperation string

const (
	OutboxOperationUpsert OutboxOperation = "upsert"
	OutboxOperationDelete OutboxOperation = "delete"
)

type PushChange struct {
	ID        string          `json:"id"`
	Entity    OutboxEntity    `json:"entity"`
	EntityID  string          `json:"entityId"`
	Operation OutboxOperation `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
	Version   string          `json:"version,omitempty"` // for conflict detection
}

// PushResult is one change's verdict. The status is accepted, conflict,
// rejected or retry_later. On a conflict, Version carries the version the
// server now holds, so the client can re-pull the entity and replay its intent.
type PushResult struct {
	EntryID string `json:"id"`
	Status  string `json:"status"` // accepted, conflict, rejected, retry_later
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

type RemoteChange struct {
	Entity    OutboxEntity    `json:"entity"`
	EntityID  string          `json:"entityId"`
	Operation OutboxOperation `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
	Version   string          `json:"version,omitempty"`
}

// PullPage is one page of the change feed. ResyncRequired is set when the
// caller's cursor points before the oldest row still retained, so it has
// missed changes and must re-pull from the start instead of trusting the page.
type PullPage struct {
	Changes        []RemoteChange `json:"changes"`
	NextCursor     string         `json:"nextCursor,omitempty"`
	HasMore        bool           `json:"hasMore"`
	ResyncRequired bool           `json:"resyncRequired,omitempty"`
}

type SyncScope string

const (
	SyncScopeProject  SyncScope = "project"
	SyncScopeWorkItem SyncScope = "workItem"
)

// ============ JSON Helpers ============

func (w *WorkItem) ToJSON() ([]byte, error) {
	return json.Marshal(w)
}

func (s *Sprint) ToJSON() ([]byte, error) {
	return json.Marshal(s)
}

func (c *Comment) ToJSON() ([]byte, error) {
	return json.Marshal(c)
}

func (a *Attachment) ToJSON() ([]byte, error) {
	return json.Marshal(a)
}

func (p *Project) ToJSON() ([]byte, error) {
	return json.Marshal(p)
}

// payloadJSON normalises a change payload to the JSON document it carries.
//
// A payload reaches this server in one of two shapes, and they mean the same
// thing:
//
//   - an embedded JSON object, which is what json.RawMessage holds when the
//     document is sent as itself. This is what the pull path re-serves and what
//     the engine's own tests build.
//   - a JSON string containing that document, which is what the mobile client
//     sends: its payload is an opaque byte array, and it encodes it with
//     decodeToString rather than base64 so the payload stays readable in a
//     server log. RawMessage then holds a quoted, escaped string.
//
// Unwrapping the second form is what stops a client push being rejected as
// unparsable. Anything that is not a JSON string is returned unchanged, so the
// object form keeps working, and a string that does not contain JSON is returned
// as its own bytes so the caller's decoder reports the real error rather than
// one invented here.
//
// Only non-delete changes reach this: a delete is applied by id and its payload,
// which is a bare id rather than a document, is never parsed.
func payloadJSON(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return data
	}
	var inner string
	if err := json.Unmarshal(trimmed, &inner); err != nil {
		return data
	}
	return []byte(inner)
}

func WorkItemFromJSON(data []byte) (*WorkItem, error) {
	var w WorkItem
	err := json.Unmarshal(payloadJSON(data), &w)
	return &w, err
}

func SprintFromJSON(data []byte) (*Sprint, error) {
	var s Sprint
	err := json.Unmarshal(payloadJSON(data), &s)
	return &s, err
}

func CommentFromJSON(data []byte) (*Comment, error) {
	var c Comment
	err := json.Unmarshal(payloadJSON(data), &c)
	return &c, err
}

func AttachmentFromJSON(data []byte) (*Attachment, error) {
	var a Attachment
	err := json.Unmarshal(payloadJSON(data), &a)
	return &a, err
}

func ProjectFromJSON(data []byte) (*Project, error) {
	var p Project
	err := json.Unmarshal(payloadJSON(data), &p)
	return &p, err
}

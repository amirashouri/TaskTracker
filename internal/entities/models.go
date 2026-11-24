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
type SprintMemberID string
type CommentID string
type AttachmentID string
type NotificationID string
type TagID string
type EpicID string

func NewUserID() UserID                 { return UserID(uuid.New().String()) }
func NewProjectID() ProjectID           { return ProjectID(uuid.New().String()) }
func NewWorkItemID() WorkItemID         { return WorkItemID(uuid.New().String()) }
func NewSprintID() SprintID             { return SprintID(uuid.New().String()) }
func NewCommentID() CommentID           { return CommentID(uuid.New().String()) }
func NewAttachmentID() AttachmentID     { return AttachmentID(uuid.New().String()) }
func NewNotificationID() NotificationID { return NotificationID(uuid.New().String()) }
func NewEpicID() EpicID                 { return EpicID(uuid.New().String()) }

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

// Presence values are the signed-in user's availability, written through
// PATCH /v1/me. They are deliberately not the Status values above: an account
// can be active and away at the same time.
const (
	PresenceActive      = "active"
	PresenceAway        = "away"
	PresenceBeRightBack = "be_right_back"
	PresenceBusy        = "busy"
)

// PresenceValues is the closed vocabulary the self-profile endpoint accepts.
var PresenceValues = []string{PresenceActive, PresenceAway, PresenceBeRightBack, PresenceBusy}

// IsValidPresence reports whether value is one of PresenceValues.
func IsValidPresence(value string) bool {
	for _, candidate := range PresenceValues {
		if candidate == value {
			return true
		}
	}
	return false
}

type User struct {
	ID           UserID `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName,omitempty"`
	PasswordHash string `json:"-"`
	Position     string `json:"position,omitempty"`
	AvatarID     string `json:"avatarId,omitempty"`
	Status       string `json:"status,omitempty"`
	// Presence and PresenceNote are what the settings screen shows as the
	// user's availability, separate from Status (the account lifecycle above).
	Presence     string `json:"presence,omitempty"`
	PresenceNote string `json:"presenceNote,omitempty"`
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

// ProjectTag is one admin-defined tag on a project. Tags are project
// configuration, so they travel and version with the project rather than in a
// table of their own; a work item references one by ID.
//
// The vocabulary is deliberately open on the server: color and icon are the
// client's design-system keys, stored verbatim so a build that knows more of
// them can add one without a migration. Unknown keys are simply not drawn.
type ProjectTag struct {
	ID    TagID  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Icon  string `json:"icon"`
}

// StatusDefinition is one admin-defined workflow status on a project.
//
// Statuses are project configuration, like ProjectTag: they travel and version
// with the project, and a work item references one by its ID. Category is the
// coarse WorkItemStatus the overdue, archive and board-column rules read, so an
// admin renaming a status or adding one never changes whether an item is
// terminal or overdue. Color is the client's design-system key, stored verbatim.
//
// omitempty on the project's slice keeps the key off the wire for a project that
// has not adopted a catalog, so an installed client that predates this field
// still decodes every response.
type StatusDefinition struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Category WorkItemStatus `json:"category"`
	Order    int            `json:"order"`
	Color    string         `json:"color"`
	// AppearsOnBoard overrides whether the status is a board column. nil means
	// "use the category's default" (Backlog/Closed are never on the board), so
	// an older client or an untargeted write gains no key and keeps working.
	AppearsOnBoard *bool `json:"appearsOnBoard,omitempty"`
}

type Project struct {
	ID        ProjectID    `json:"id"`
	KeyPrefix string       `json:"keyPrefix"`
	Name      string       `json:"name"`
	Details   string       `json:"details"`
	Tags      []ProjectTag `json:"tags"`
	// RepoURL is where the project's code lives. It is omitted when empty, so a
	// project without a repository gains no key. The KMP client now declares
	// `repoUrl` as an optional field with a default, which is what makes emitting
	// it safe; the agent run graph also carries it explicitly.
	RepoURL string `json:"repoUrl,omitempty"`
	// Statuses is the project's workflow catalog. Omitted when empty so a
	// response gains no key for a project that has not adopted one.
	Statuses   []StatusDefinition `json:"statuses,omitempty"`
	MemberIDs  []UserID           `json:"memberIds"`
	IsArchived bool               `json:"isArchived"`
	// AgentEnabled is the admin's per-project switch for whether a work item here
	// may be dispatched to an agent. It is project-scoped, not server-scoped.
	AgentEnabled   bool      `json:"agentEnabled"`
	NextItemNumber int       `json:"nextItemNumber"`
	CreatedAt      Timestamp `json:"createdAt"`
	UpdatedAt      Timestamp `json:"updatedAt"`
	Version        string    `json:"version,omitempty"`
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
	TagIDs       []TagID        `json:"tagIDs"`
	// StatusKey names the project's StatusDefinition this item sits in. Empty is
	// "no catalog adopted" and the client derives the column from Status; omitted
	// when empty so an installed client that predates the field still decodes.
	StatusKey string    `json:"statusKey,omitempty"`
	SprintID  *SprintID `json:"sprintId,omitempty"`
	// EpicID links the item to at most one epic. The epic's status and progress
	// are derived from its items, so nothing about them is stored here. Omitted
	// when unset, which is what keeps the key off a work item that has no epic.
	EpicID     *EpicID    `json:"epicID,omitempty"`
	Rank       int        `json:"rank"`
	CreatedAt  Timestamp  `json:"createdAt"`
	UpdatedAt  Timestamp  `json:"updatedAt"`
	ResolvedAt *Timestamp `json:"resolvedAt,omitempty"`
	Version    string     `json:"version"` // optimistic lock
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
	IsLocked        bool       `json:"isLocked"`
	CreatedAt       Timestamp  `json:"createdAt"`
	UpdatedAt       Timestamp  `json:"updatedAt"`
	Version         string     `json:"version"`
}

// SprintMember is one person's stated capacity in a sprint. The id is minted by
// the client from the sprint and user ids, so a re-push updates the same row
// instead of creating a second membership. Capacity is advisory: the client
// warns when it exceeds the sprint goal rather than refusing the write.
type SprintMember struct {
	ID                   SprintMemberID `json:"id"`
	ProjectID            ProjectID      `json:"projectID"`
	SprintID             SprintID       `json:"sprintID"`
	UserID               UserID         `json:"userID"`
	AvailableStoryPoints int            `json:"availableStoryPoints"`
	CreatedAt            Timestamp      `json:"createdAt"`
	UpdatedAt            Timestamp      `json:"updatedAt"`
	Version              string         `json:"version"`
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

// ============ Epic ============

// Epic groups work items across two or more projects. Its status and progress
// are derived from its items on the client and never stored or synced: a stored
// status would be a second answer that drifts the moment an item moves, and a
// stored count would leak work the reader cannot see.
//
// ProjectIDs is the set of projects the epic spans, in the order the client
// chose. It is also the epic's visibility set: the change feed writes one
// visibility row per project so every member of every linked project receives
// the epic.
type Epic struct {
	ID         EpicID      `json:"id"`
	ProjectIDs []ProjectID `json:"projectIds"`
	// OwnerID is who answers for the epic. It is omitted only when the server
	// has no admin to fall back to; a normal row always carries one.
	OwnerID    *UserID    `json:"ownerId,omitempty"`
	Title      string     `json:"title"`
	Details    string     `json:"details"`
	DueDate    *Timestamp `json:"dueDate,omitempty"`
	IsArchived bool       `json:"isArchived"`
	CreatedAt  Timestamp  `json:"createdAt"`
	UpdatedAt  Timestamp  `json:"updatedAt"`
	Version    string     `json:"version"`
}

// ProjectIDSet is the epic's project list with duplicates removed, preserving
// order. The wire may repeat an id; the join table cannot.
func (e *Epic) ProjectIDSet() []ProjectID {
	seen := make(map[ProjectID]struct{}, len(e.ProjectIDs))
	out := make([]ProjectID, 0, len(e.ProjectIDs))
	for _, id := range e.ProjectIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

// ============ Notification ============

// The two events this server generates. A kind is a value, not a key, so a new
// one never changes the notification object's shape.
const (
	NotificationKindTaskAssigned = "task_assigned"
	NotificationKindCommentAdded = "comment_added"
)

// What produced a notification. Together with SourceID it is the idempotency
// key of generation: a replayed change cannot create a second row for the same
// event.
const (
	NotificationSourceWorkItem = "workItem"
	NotificationSourceComment  = "comment"
)

// Notification is one inbox row, server-generated for a single recipient.
//
// The wire object is deliberately narrow: the KMP client declares exactly these
// keys and its decoder leaves `ignoreUnknownKeys` off, so a gained key is a
// decoding failure. `Payload` is a JSON *string* holding the kind-specific
// document, because that is how the client's model declares it — the same
// double encoding a sync change's payload uses.
//
// Version, SourceKind, SourceID and DismissedAt are storage concerns and are
// never on the wire.
type Notification struct {
	ID        NotificationID `json:"id"`
	UserID    UserID         `json:"userID"`
	Kind      string         `json:"kind"`
	Payload   string         `json:"payload"`
	IsRead    bool           `json:"isRead"`
	CreatedAt Timestamp      `json:"createdAt"`
	UpdatedAt Timestamp      `json:"updatedAt"`

	Version     string     `json:"-"`
	SourceKind  string     `json:"-"`
	SourceID    string     `json:"-"`
	DismissedAt *Timestamp `json:"-"`
}

// NotificationPayload is the kind-specific document inside Notification.Payload.
// It carries enough for a card and its navigation target without a join: the
// task's key and title, the project, and who acted.
type NotificationPayload struct {
	WorkItemID       string `json:"workItemId"`
	WorkItemKey      string `json:"workItemKey"`
	TaskTitle        string `json:"taskTitle"`
	ProjectID        string `json:"projectId"`
	ProjectKeyPrefix string `json:"projectKeyPrefix"`
	ProjectName      string `json:"projectName"`
	ActorName        string `json:"actorName"`
	CommentID        string `json:"commentId,omitempty"`
	CommentPreview   string `json:"commentPreview,omitempty"`
}

// ============ Sync Types ============

type OutboxEntity string

const (
	OutboxEntityProject      OutboxEntity = "project"
	OutboxEntityWorkItem     OutboxEntity = "workItem"
	OutboxEntitySprint       OutboxEntity = "sprint"
	OutboxEntitySprintMember OutboxEntity = "sprintMember"
	OutboxEntityComment      OutboxEntity = "comment"
	OutboxEntityAttachment   OutboxEntity = "attachment"
	OutboxEntityEpic         OutboxEntity = "epic"
	// Notifications are the one entity a client never creates: a push may only
	// change the caller's own read state or dismiss its own row.
	OutboxEntityNotification OutboxEntity = "notification"
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
	// ProjectRepoURL carries a repository URL supplied out of band: the admin
	// route is the only way to change one after creation. It is not a wire field
	// of its own and never reaches the change feed — the feed's project object
	// must not gain a key an installed client cannot decode. A membership push
	// leaves it nil, which tells the engine to preserve the stored value. A
	// project being created may name its own URL in the payload; see applyProject.
	ProjectRepoURL *string `json:"-"`
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

func (e *Epic) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

func (p *Project) ToJSON() ([]byte, error) {
	return json.Marshal(p)
}

func (n *Notification) ToJSON() ([]byte, error) {
	return json.Marshal(n)
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

func SprintMemberFromJSON(data []byte) (*SprintMember, error) {
	var m SprintMember
	err := json.Unmarshal(payloadJSON(data), &m)
	return &m, err
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

func EpicFromJSON(data []byte) (*Epic, error) {
	var e Epic
	err := json.Unmarshal(payloadJSON(data), &e)
	return &e, err
}

func ProjectFromJSON(data []byte) (*Project, error) {
	var p Project
	err := json.Unmarshal(payloadJSON(data), &p)
	return &p, err
}

// NotificationFromJSON decodes one notification document. The client sends the
// full document on a read-state push, so a decode that ignores storage-only
// fields is enough here.
func NotificationFromJSON(data []byte) (*Notification, error) {
	var n Notification
	err := json.Unmarshal(payloadJSON(data), &n)
	return &n, err
}

// NotificationReadStateFromJSON reads the only field a push is allowed to
// change. A nil answer means the key was absent, which the engine rejects rather
// than guessing "unread".
func NotificationReadStateFromJSON(data []byte) (*bool, error) {
	var patch struct {
		IsRead *bool `json:"isRead"`
	}
	if err := json.Unmarshal(payloadJSON(data), &patch); err != nil {
		return nil, err
	}
	return patch.IsRead, nil
}

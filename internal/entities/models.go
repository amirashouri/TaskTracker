package entities

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ============ Core Types ============

type UserID string
type ProjectID string
type WorkItemID string
type SprintID string
type CommentID string

func NewUserID() UserID         { return UserID(uuid.New().String()) }
func NewProjectID() ProjectID   { return ProjectID(uuid.New().String()) }
func NewWorkItemID() WorkItemID { return WorkItemID(uuid.New().String()) }
func NewSprintID() SprintID     { return SprintID(uuid.New().String()) }
func NewCommentID() CommentID   { return CommentID(uuid.New().String()) }

// ============ User ============

type User struct {
	ID           UserID `json:"id"`
	Email        string `json:"email"`
	DisplayName  string `json:"displayName,omitempty"`
	PasswordHash string `json:"-"`
	Position     string `json:"position,omitempty"`
	AvatarID     string `json:"avatarId,omitempty"`
	Status       string `json:"status,omitempty"`
	LastSeen     *int64 `json:"lastSeen,omitempty"` // epoch millis
	CreatedAt    int64  `json:"createdAt"`
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
	CreatedAt      int64     `json:"createdAt"` // epoch millis
	UpdatedAt      int64     `json:"updatedAt"`
}

func (p *Project) NextKey() string {
	key := p.KeyPrefix + "-" + string(rune(p.NextItemNumber+'0')) // simplified
	p.NextItemNumber++
	return key
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
	Type  string `json:"type"` // daily, weekly, monthly, yearly
	Day   int    `json:"day,omitempty"`
	Until *int64 `json:"until,omitempty"` // epoch millis
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
	DueDate      *int64         `json:"dueDate,omitempty"` // epoch millis
	Recurrence   *Recurrence    `json:"recurrence,omitempty"`
	StoryPoints  *int           `json:"storyPoints,omitempty"`
	BlockedByIDs []WorkItemID   `json:"blockedByIds"`
	SprintID     *SprintID      `json:"sprintId,omitempty"`
	Rank         int            `json:"rank"`
	CreatedAt    int64          `json:"createdAt"`
	UpdatedAt    int64          `json:"updatedAt"`
	ResolvedAt   *int64         `json:"resolvedAt,omitempty"`
	Version      string         `json:"version"` // optimistic lock
}

func (w *WorkItem) IsTerminal() bool {
	return w.Status == WorkItemStatusDone || w.Status == WorkItemStatusClosed
}

func (w *WorkItem) IsOverdue(now int64) bool {
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
	ID              SprintID  `json:"id"`
	ProjectID       ProjectID `json:"projectId"`
	Name            string    `json:"name"`
	GoalStoryPoints int       `json:"goalStoryPoints"`
	Goal            string    `json:"goal,omitempty"`
	StartDate       int64     `json:"startDate"` // epoch millis
	EndDate         int64     `json:"endDate"`   // epoch millis
	OwnerID         *UserID   `json:"ownerId,omitempty"`
	IsFinished      bool      `json:"isFinished"`
	FinishedAt      *int64    `json:"finishedAt,omitempty"`
	IsArchived      bool      `json:"isArchived"`
	CreatedAt       int64     `json:"createdAt"`
	UpdatedAt       int64     `json:"updatedAt"`
	Version         string    `json:"version"`
}

func (s *Sprint) StatusAt(now int64) SprintStatus {
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
	CreatedAt  int64      `json:"createdAt"`
	ParentID   *CommentID `json:"parentId,omitempty"`
	Version    string     `json:"version"`
}

// ============ Sync Types ============

type OutboxEntity string

const (
	OutboxEntityProject  OutboxEntity = "project"
	OutboxEntityWorkItem OutboxEntity = "workItem"
	OutboxEntitySprint   OutboxEntity = "sprint"
	OutboxEntityComment  OutboxEntity = "comment"
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

type PullPage struct {
	Changes    []RemoteChange `json:"changes"`
	NextCursor string         `json:"nextCursor,omitempty"`
	HasMore    bool           `json:"hasMore"`
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

func (p *Project) ToJSON() ([]byte, error) {
	return json.Marshal(p)
}

func WorkItemFromJSON(data []byte) (*WorkItem, error) {
	var w WorkItem
	err := json.Unmarshal(data, &w)
	return &w, err
}

func SprintFromJSON(data []byte) (*Sprint, error) {
	var s Sprint
	err := json.Unmarshal(data, &s)
	return &s, err
}

func CommentFromJSON(data []byte) (*Comment, error) {
	var c Comment
	err := json.Unmarshal(data, &c)
	return &c, err
}

func ProjectFromJSON(data []byte) (*Project, error) {
	var p Project
	err := json.Unmarshal(data, &p)
	return &p, err
}

package agent

import (
	"context"
	"encoding/json"

	"github.com/amirashouri/tasktracker-server/internal/entities"
)

// The agent brief is a bounded, versioned graph rather than a dump of the
// project. The bounds are stated in the payload itself so a newer agent can see
// what it was given, and a newer server can raise them without guessing what an
// older agent expected.
const (
	contextGraphSchemaVersion = 1
	maxContextNodes           = 64
	maxContextTags            = 20
	maxContextDependencies    = 10
	maxContextComments        = 20
	maxCommentBodyRunes       = 4000
	maxTaskDetailsRunes       = 20000
)

const (
	nodeProject = "project"
	nodeTask    = "task"
	nodeTag     = "tag"
	nodeComment = "comment"
)

// Edge direction is part of the contract, so it is spelled once:
//
//	task -> prerequisite   DEPENDS_ON
//	task -> project        BELONGS_TO
//	task -> tag            TAGGED_WITH
//	task -> comment        HAS_COMMENT
const (
	edgeDependsOn  = "DEPENDS_ON"
	edgeBelongsTo  = "BELONGS_TO"
	edgeTaggedWith = "TAGGED_WITH"
	edgeHasComment = "HAS_COMMENT"
)

// contextGraph is the JSON stored in agent_runs.task_document. It is opaque to
// the app (the app's run response never carries it); the worker receives it
// verbatim on claim.
type contextGraph struct {
	SchemaVersion int          `json:"schemaVersion"`
	RunID         string       `json:"runId"`
	Project       graphProject `json:"project"`
	Task          graphTask    `json:"task"`
	Nodes         []graphNode  `json:"nodes"`
	Edges         []graphEdge  `json:"edges"`
	Limits        graphLimits  `json:"limits"`
}

type graphProject struct {
	ID        entities.ProjectID `json:"id"`
	KeyPrefix string             `json:"keyPrefix"`
	Name      string             `json:"name"`
	// RepoURL is project metadata, not a credential. Private access is the
	// agent's own business, which is why nothing secret is stored here.
	RepoURL string `json:"repoUrl,omitempty"`
	// TargetBranch is omitted until project metadata carries one; an absent key
	// is the honest "not known", not an instruction to guess.
	TargetBranch string `json:"targetBranch,omitempty"`
}

type graphTask struct {
	ID          entities.WorkItemID `json:"id"`
	Key         string              `json:"key"`
	Title       string              `json:"title"`
	Details     string              `json:"details"`
	Status      string              `json:"status"`
	Priority    string              `json:"priority"`
	StoryPoints *int                `json:"storyPoints,omitempty"`
	DueDate     *entities.Timestamp `json:"dueDate,omitempty"`
	Tags        []string            `json:"tags"`
}

// graphNode is one bounded unit of context. IDs are stable and namespaced
// (`task:<uuid>`, `tag:<id>`, `comment:<uuid>`), so an agent can correlate the
// same entity across runs without a second lookup.
type graphNode struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Key    string `json:"key,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	Body   string `json:"body,omitempty"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type graphLimits struct {
	MaxNodes            int `json:"maxNodes"`
	MaxTags             int `json:"maxTags"`
	MaxDependencies     int `json:"maxDependencies"`
	MaxComments         int `json:"maxComments"`
	MaxCommentRunes     int `json:"maxCommentRunes"`
	MaxTaskDetailsRunes int `json:"maxTaskDetailsRunes"`
}

// buildContextGraph assembles the task-focused subgraph a run receives.
//
// It is permission-filtered by construction: the dispatch handler has already
// established that the requester is a member of the project, and this builder
// only ever reads that project, the task, its direct prerequisites, its tags and
// its bounded comment thread. No other project's history, attachments, sprints
// or unrelated comments enter the payload.
func (h *AppHandler) buildContextGraph(ctx context.Context, runID entities.AgentRunID, item entities.WorkItem, project entities.Project) (string, error) {
	taskID := taskNodeID(item.ID)
	projectNodeID := projectNodeID(project.ID)

	nodes := make([]graphNode, 0, maxContextNodes)
	edges := make([]graphEdge, 0, maxContextNodes)
	nodes = append(nodes,
		graphNode{ID: projectNodeID, Type: nodeProject, Key: project.KeyPrefix, Title: project.Name},
		graphNode{
			ID:     taskID,
			Type:   nodeTask,
			Key:    item.Key,
			Title:  item.Title,
			Status: string(item.Status),
			Body:   truncateRunes(item.Details, maxTaskDetailsRunes),
		},
	)
	edges = append(edges, graphEdge{From: taskID, To: projectNodeID, Type: edgeBelongsTo})

	catalogue := make(map[entities.TagID]string, len(project.Tags))
	for _, tag := range project.Tags {
		catalogue[tag.ID] = tag.Name
	}
	tagNames := make([]string, 0, len(item.TagIDs))
	for _, id := range item.TagIDs {
		if len(tagNames) >= maxContextTags {
			break
		}
		name, ok := catalogue[id]
		if !ok || id == "" {
			continue
		}
		tagNames = append(tagNames, name)
		id := tagNodeID(id)
		nodes = append(nodes, graphNode{ID: id, Type: nodeTag, Title: name})
		edges = append(edges, graphEdge{From: taskID, To: id, Type: edgeTaggedWith})
	}

	dependencyIDs := item.BlockedByIDs
	if len(dependencyIDs) > maxContextDependencies {
		dependencyIDs = dependencyIDs[:maxContextDependencies]
	}
	if len(dependencyIDs) > 0 {
		dependencies, err := h.workItems.GetByIDs(ctx, dependencyIDs)
		if err != nil {
			return "", err
		}
		byID := make(map[entities.WorkItemID]*entities.WorkItem, len(dependencies))
		for _, dependency := range dependencies {
			byID[dependency.ID] = dependency
		}
		for _, id := range dependencyIDs {
			dependency := byID[id]
			// A blocker in another project is not this run's context, even if a
			// stale row says it is; the project filter is the permission filter.
			if dependency == nil || dependency.ProjectID != item.ProjectID {
				continue
			}
			dependencyNodeID := taskNodeID(dependency.ID)
			nodes = append(nodes, graphNode{
				ID:     dependencyNodeID,
				Type:   nodeTask,
				Key:    dependency.Key,
				Title:  dependency.Title,
				Status: string(dependency.Status),
			})
			edges = append(edges, graphEdge{From: taskID, To: dependencyNodeID, Type: edgeDependsOn})
		}
	}

	comments, err := h.comments.ListRecentByWorkItem(ctx, item.ID, maxContextComments)
	if err != nil {
		return "", err
	}
	// Newest-first is the read; the brief reads chronologically.
	for left, right := 0, len(comments)-1; left < right; left, right = left+1, right-1 {
		comments[left], comments[right] = comments[right], comments[left]
	}
	authors := make(map[entities.UserID]string, len(comments))
	for _, comment := range comments {
		author, seen := authors[comment.AuthorID]
		if !seen {
			user, err := h.users.GetByID(ctx, comment.AuthorID)
			if err != nil {
				return "", err
			}
			if user != nil {
				author = user.PreferredGreeting()
			}
			authors[comment.AuthorID] = author
		}
		id := commentNodeID(comment.ID)
		nodes = append(nodes, graphNode{
			ID:    id,
			Type:  nodeComment,
			Title: author,
			Body:  truncateRunes(comment.Body, maxCommentBodyRunes),
		})
		edges = append(edges, graphEdge{From: taskID, To: id, Type: edgeHasComment})
	}

	nodes, edges = trimGraph(nodes, edges, maxContextNodes)

	document := contextGraph{
		SchemaVersion: contextGraphSchemaVersion,
		RunID:         string(runID),
		Project: graphProject{
			ID:        project.ID,
			KeyPrefix: project.KeyPrefix,
			Name:      project.Name,
			RepoURL:   project.RepoURL,
		},
		Task: graphTask{
			ID:          item.ID,
			Key:         item.Key,
			Title:       item.Title,
			Details:     truncateRunes(item.Details, maxTaskDetailsRunes),
			Status:      string(item.Status),
			Priority:    string(item.Priority),
			StoryPoints: item.StoryPoints,
			DueDate:     item.DueDate,
			Tags:        tagNames,
		},
		Nodes: nodes,
		Edges: edges,
		Limits: graphLimits{
			MaxNodes:            maxContextNodes,
			MaxTags:             maxContextTags,
			MaxDependencies:     maxContextDependencies,
			MaxComments:         maxContextComments,
			MaxCommentRunes:     maxCommentBodyRunes,
			MaxTaskDetailsRunes: maxTaskDetailsRunes,
		},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func taskNodeID(id entities.WorkItemID) string   { return "task:" + string(id) }
func projectNodeID(id entities.ProjectID) string { return "project:" + string(id) }
func tagNodeID(id entities.TagID) string         { return "tag:" + string(id) }
func commentNodeID(id entities.CommentID) string { return "comment:" + string(id) }

// trimGraph enforces the node cap and drops edges whose endpoints did not
// survive. It should be unreachable while the per-kind caps stay under
// maxContextNodes, but an edge to a node that is not in the payload would make
// the graph lie about its own shape.
func trimGraph(nodes []graphNode, edges []graphEdge, max int) ([]graphNode, []graphEdge) {
	if len(nodes) <= max {
		return nodes, edges
	}
	nodes = nodes[:max]
	kept := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		kept[node.ID] = struct{}{}
	}
	trimmed := edges[:0]
	for _, edge := range edges {
		_, from := kept[edge.From]
		_, to := kept[edge.To]
		if from && to {
			trimmed = append(trimmed, edge)
		}
	}
	return nodes, trimmed
}

// truncateRunes bounds a comment body without splitting a multi-byte character.
func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

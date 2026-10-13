package entities

import (
	"encoding/json"
	"testing"
)

// The mobile client sends a change payload as a JSON *string* holding the
// document — its payload is an opaque byte array it encodes with decodeToString
// rather than base64 — while the pull path and this engine's own tests send the
// document as itself. Both have to decode, because the server parses a payload
// to work out which project a write belongs to before it authorizes it.
func TestFromJSONAcceptsAStringEncodedPayload(t *testing.T) {
	// Exactly what the client's encoder produces for a work item.
	encoded, err := json.Marshal(`{"title":"from client","projectId":"p-1"}`)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	item, err := WorkItemFromJSON(encoded)
	if err != nil {
		t.Fatalf("a string-encoded payload was rejected: %v", err)
	}
	if item.Title != "from client" {
		t.Errorf("title = %q, want %q", item.Title, "from client")
	}
	if item.ProjectID != "p-1" {
		t.Errorf("project id = %q, want p-1", item.ProjectID)
	}
}

func TestFromJSONStillAcceptsAnEmbeddedObjectPayload(t *testing.T) {
	item, err := WorkItemFromJSON([]byte(`{"title":"from server"}`))
	if err != nil {
		t.Fatalf("an embedded-object payload was rejected: %v", err)
	}
	if item.Title != "from server" {
		t.Errorf("title = %q, want %q", item.Title, "from server")
	}
}

// Unwrapping must not turn a payload that is genuinely not a document into a
// silent success.
func TestFromJSONRejectsAStringThatIsNotADocument(t *testing.T) {
	if _, err := WorkItemFromJSON([]byte(`"not a document"`)); err == nil {
		t.Error("a string payload that holds no JSON should not decode")
	}
}

// Every entity a client can push goes through the same unwrapping, so a change
// to one helper cannot quietly leave another behind.
func TestFromJSONAcceptsAStringPayloadForEveryEntity(t *testing.T) {
	cases := []struct {
		entity string
		fields map[string]any
		decode func([]byte) error
	}{
		{
			entity: "project",
			fields: map[string]any{"name": "Demo"},
			decode: func(b []byte) error { _, err := ProjectFromJSON(b); return err },
		},
		{
			entity: "sprint",
			fields: map[string]any{"name": "Sprint 1"},
			decode: func(b []byte) error { _, err := SprintFromJSON(b); return err },
		},
		{
			entity: "comment",
			fields: map[string]any{"body": "hello"},
			decode: func(b []byte) error { _, err := CommentFromJSON(b); return err },
		},
	}

	for _, tc := range cases {
		t.Run(tc.entity, func(t *testing.T) {
			document, err := json.Marshal(tc.fields)
			if err != nil {
				t.Fatalf("marshal fields: %v", err)
			}
			encoded, err := json.Marshal(string(document))
			if err != nil {
				t.Fatalf("marshal string: %v", err)
			}

			if err := tc.decode(encoded); err != nil {
				t.Errorf("a string-encoded %s payload was rejected: %v", tc.entity, err)
			}
		})
	}
}

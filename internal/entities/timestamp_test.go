package entities

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// instant is the whole-second instant most of these cases are built around.
func instant(year int, month time.Month, day, hour, min, sec int) Timestamp {
	return secondPrecision(time.Date(year, month, day, hour, min, sec, 0, time.UTC).UnixMilli())
}

func TestTimestampMarshalsToWholeSecondISO8601(t *testing.T) {
	cases := []struct {
		name  string
		value Timestamp
		want  string
	}{
		{"a whole second", instant(2026, time.September, 26, 20, 13, 32), `"2026-09-26T20:13:32Z"`},
		{
			"sub-second millis are dropped",
			Timestamp(time.Date(2026, time.September, 26, 20, 13, 32, 123_000_000, time.UTC).UnixMilli()),
			`"2026-09-26T20:13:32Z"`,
		},
		{"the epoch", 0, `"1970-01-01T00:00:00Z"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("marshal = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTimestampUnmarshalsWhatTheClientSends(t *testing.T) {
	want := instant(2026, time.September, 26, 20, 13, 32)

	cases := []struct {
		name string
		raw  string
	}{
		{"the canonical form", `"2026-09-26T20:13:32Z"`},
		{"fractional seconds are truncated, not rejected", `"2026-09-26T20:13:32.500Z"`},
		{"an explicit zero offset", `"2026-09-26T20:13:32+00:00"`},
		{"a negative zero offset", `"2026-09-26T20:13:32-00:00"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Timestamp
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if got != want {
				t.Errorf("unmarshal %s = %s, want %s", tc.raw, got, want)
			}
		})
	}
}

// A non-UTC offset is a legitimate ISO-8601 instant and must land on the same
// second, not on a value shifted by the offset.
func TestTimestampUnmarshalsANonUTCOffset(t *testing.T) {
	var got Timestamp
	if err := json.Unmarshal([]byte(`"2026-09-26T23:43:32+03:30"`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := instant(2026, time.September, 26, 20, 13, 32); got != want {
		t.Errorf("unmarshal = %s, want %s", got, want)
	}
}

// A bare number is not what the client sends, but a hand-rolled curl should not
// have to know that. The value is milliseconds, matching the columns.
func TestTimestampUnmarshalsABareEpochNumber(t *testing.T) {
	var got Timestamp
	if err := json.Unmarshal([]byte(`1800000000123`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := Timestamp(1800000000000); got != want {
		t.Errorf("unmarshal = %s, want %s", got, want)
	}
}

// encoding/json hands a JSON null to UnmarshalJSON, so it has to mean absent
// rather than an error.
func TestTimestampUnmarshalsNullAsTheZeroValue(t *testing.T) {
	got := instant(2026, time.September, 26, 20, 13, 32)
	if err := json.Unmarshal([]byte(`null`), &got); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("unmarshal null = %s, want the zero value", got)
	}
}

func TestTimestampRejectsWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"a date with no time", `"2026-09-26"`},
		{"an instant with no zone, which is ambiguous", `"2026-09-26T20:13:32"`},
		{"a word", `"yesterday"`},
		{"a boolean", `true`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Timestamp
			if err := json.Unmarshal([]byte(tc.raw), &got); err == nil {
				t.Errorf("unmarshal %s succeeded with %s, want an error", tc.raw, got)
			}
		})
	}
}

// The whole point of pinning the format: a value the server writes must survive
// a trip through the client and come back as the same instant. A lossy operand
// would make "which row is newer" arbitrary, which is what the client's
// DateCoding rules out.
func TestTimestampRoundTripsWithoutLoss(t *testing.T) {
	values := []Timestamp{
		instant(2026, time.September, 26, 20, 13, 32),
		instant(1970, time.January, 1, 0, 0, 0),
		instant(2038, time.January, 19, 3, 14, 7),
		Now(),
	}

	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", value, err)
		}

		var decoded Timestamp
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal %s: %v", encoded, err)
		}
		if decoded != value {
			t.Errorf("%s round-tripped as %s (via %s)", value, decoded, encoded)
		}
	}
}

// Now has to satisfy the same invariant as everything else, or the server would
// write a value it cannot hand back.
func TestNowIsWholeSeconds(t *testing.T) {
	now := Now()
	if now%1000 != 0 {
		t.Errorf("Now() = %d, which is not a whole second", int64(now))
	}
	if elapsed := time.Since(now.Time()); elapsed > time.Minute || elapsed < -time.Minute {
		t.Errorf("Now() = %s, which is not close to the current time", now)
	}
}

// Every synced entity carries timestamps, so a format that works for one and
// not another is not a format. This walks each field the wire exposes.
func TestSyncedEntitiesRenderTimestampsAsISO8601(t *testing.T) {
	when := instant(2026, time.September, 26, 20, 13, 32)
	const want = "2026-09-26T20:13:32Z"

	cases := []struct {
		name   string
		value  any
		fields []string
	}{
		{"project", &Project{CreatedAt: when, UpdatedAt: when}, []string{"createdAt", "updatedAt"}},
		{
			"workItem",
			&WorkItem{CreatedAt: when, UpdatedAt: when, DueDate: &when, ResolvedAt: &when},
			[]string{"createdAt", "updatedAt", "dueDate", "resolvedAt"},
		},
		{
			"sprint",
			&Sprint{CreatedAt: when, UpdatedAt: when, StartDate: when, EndDate: when, FinishedAt: &when},
			[]string{"createdAt", "updatedAt", "startDate", "endDate", "finishedAt"},
		},
		{"comment", &Comment{CreatedAt: when}, []string{"createdAt"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}

			for _, field := range tc.fields {
				got, ok := decoded[field]
				if !ok {
					t.Errorf("%s is missing from %s", field, raw)
					continue
				}
				if got != want {
					t.Errorf("%s = %v, want %q (a JSON string, not a number)", field, got, want)
				}
			}
		})
	}
}

func TestRecurrenceUntilRendersAsISO8601(t *testing.T) {
	when := instant(2026, time.September, 26, 20, 13, 32)
	raw, err := json.Marshal(&Recurrence{Type: "weekly", Day: 2, Until: &when})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"until":"2026-09-26T20:13:32Z"`) {
		t.Errorf("recurrence = %s, want until as an ISO-8601 string", raw)
	}
}

// An absent optional timestamp stays absent rather than becoming the epoch,
// which is what omitempty on a pointer is for.
func TestAbsentOptionalTimestampsAreOmitted(t *testing.T) {
	raw, err := json.Marshal(&WorkItem{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{"dueDate", "resolvedAt"} {
		if strings.Contains(string(raw), field) {
			t.Errorf("%s is present in %s, want it omitted", field, raw)
		}
	}
}

// The server's own ToJSON has to emit what its FromJSON accepts, for all four
// entities, or a change the server originates cannot be read back by a client.
func TestEntitiesRoundTripThroughToAndFromJSON(t *testing.T) {
	when := instant(2026, time.September, 26, 20, 13, 32)
	later := instant(2026, time.September, 27, 9, 0, 0)

	t.Run("project", func(t *testing.T) {
		original := &Project{
			ID: "p-1", KeyPrefix: "TT", Name: "Demo", MemberIDs: []UserID{"u-1"},
			CreatedAt: when, UpdatedAt: later, Version: "v-1",
		}
		assertRoundTrips(t, original, original.ToJSON, func(raw []byte) (*Project, error) {
			return ProjectFromJSON(raw)
		}, func(a, b *Project) bool {
			return a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt && a.Name == b.Name
		})
	})

	t.Run("workItem", func(t *testing.T) {
		original := &WorkItem{
			ID: "w-1", ProjectID: "p-1", Key: "TT-1", Title: "Task",
			Status: WorkItemStatusTodo, Priority: PriorityMedium,
			DueDate: &when, ResolvedAt: &later, CreatedAt: when, UpdatedAt: later, Version: "v-1",
		}
		assertRoundTrips(t, original, original.ToJSON, func(raw []byte) (*WorkItem, error) {
			return WorkItemFromJSON(raw)
		}, func(a, b *WorkItem) bool {
			return a.CreatedAt == b.CreatedAt && a.UpdatedAt == b.UpdatedAt &&
				*a.DueDate == *b.DueDate && *a.ResolvedAt == *b.ResolvedAt
		})
	})

	t.Run("sprint", func(t *testing.T) {
		original := &Sprint{
			ID: "s-1", ProjectID: "p-1", Name: "Sprint 1",
			StartDate: when, EndDate: later, FinishedAt: &later,
			CreatedAt: when, UpdatedAt: later, Version: "v-1",
		}
		assertRoundTrips(t, original, original.ToJSON, func(raw []byte) (*Sprint, error) {
			return SprintFromJSON(raw)
		}, func(a, b *Sprint) bool {
			return a.StartDate == b.StartDate && a.EndDate == b.EndDate &&
				a.CreatedAt == b.CreatedAt && *a.FinishedAt == *b.FinishedAt
		})
	})

	t.Run("comment", func(t *testing.T) {
		original := &Comment{
			ID: "c-1", WorkItemID: "w-1", AuthorID: "u-1", Body: "first",
			CreatedAt: when, Version: "v-1",
		}
		assertRoundTrips(t, original, original.ToJSON, func(raw []byte) (*Comment, error) {
			return CommentFromJSON(raw)
		}, func(a, b *Comment) bool {
			return a.CreatedAt == b.CreatedAt && a.Body == b.Body
		})
	})
}

// assertRoundTrips encodes, decodes and compares, and separately checks that the
// encoded form really did carry an ISO-8601 string rather than a number.
func assertRoundTrips[T any](
	t *testing.T,
	original *T,
	encode func() ([]byte, error),
	decode func([]byte) (*T, error),
	equal func(a, b *T) bool,
) {
	t.Helper()

	raw, err := encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(raw), `"createdAt":"2026-09-26T20:13:32Z"`) {
		t.Errorf("encoded form carries no ISO-8601 createdAt: %s", raw)
	}

	back, err := decode(raw)
	if err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !equal(original, back) {
		t.Errorf("round trip changed the value:\n original %+v\n decoded  %+v", original, back)
	}
}

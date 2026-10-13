package entities

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Timestamp is a point in time as it crosses the wire: ISO-8601 UTC at
// whole-second resolution, for example 2026-09-26T20:13:32Z.
//
// The format is not this server's to choose. The mobile client fixes it in its
// own DateCoding and says why: sub-second precision cannot be encoded and read
// back without loss, so a "which row is newer" comparison run against a lossy
// operand is arbitrary. A server that stamped milliseconds would write one
// instant, have the client round it, and receive back a number it never wrote.
//
// The value is epoch milliseconds, which is what the columns hold and what the
// rest of the package compares, but it is always a whole number of seconds:
// Now, UnmarshalJSON and timestampOrNil all truncate. The invariant therefore
// holds however a value entered, and marshalling is a faithful inverse of
// unmarshalling — a stored instant survives a round trip through the client
// unchanged.
//
// Only the synced entities use it. A User's timestamps belong to the auth and
// admin API, which is a separate contract with its own snake_case shape.
type Timestamp int64

// Now is the current instant, truncated to the resolution the wire carries.
func Now() Timestamp {
	return secondPrecision(time.Now().UnixMilli())
}

// secondPrecision drops the sub-second part of an epoch-millisecond value,
// rounding down the way time.Truncate does rather than toward zero.
func secondPrecision(millis int64) Timestamp {
	return Timestamp(time.UnixMilli(millis).Truncate(time.Second).UnixMilli())
}

// Time returns the instant in UTC.
func (t Timestamp) Time() time.Time {
	return time.UnixMilli(int64(t)).UTC()
}

// IsZero reports whether the timestamp was never set. The zero value means
// absent rather than 1970, which is what lets an omitted field stay omitted.
func (t Timestamp) IsZero() bool {
	return t == 0
}

// String renders the wire form, so a log line and a payload agree.
func (t Timestamp) String() string {
	return t.Time().Format(time.RFC3339)
}

// MarshalJSON writes the whole-second ISO-8601 form. time.RFC3339 renders a UTC
// instant with a trailing Z and no fractional part, which is exactly what the
// client's DateCoding.format produces.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// UnmarshalJSON accepts the ISO-8601 string the client sends, and a bare epoch
// number as well so a hand-rolled curl keeps working.
//
// A fractional second is truncated rather than rejected. The client tolerates
// one on the way in, and rounding here is what keeps the value this server
// stores equal to the value it will hand back.
func (t *Timestamp) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)

	// encoding/json calls this for a JSON null too, including on a non-pointer
	// field. Absent is the zero value.
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*t = 0
		return nil
	}

	if trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		// The layout carries no fractional second, but time.Parse accepts one
		// anyway when it follows the seconds, which is what the client may send.
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("timestamp %q is not ISO-8601: %w", raw, err)
		}
		*t = secondPrecision(parsed.UnixMilli())
		return nil
	}

	millis, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return fmt.Errorf("timestamp %s is neither an ISO-8601 string nor an epoch number", trimmed)
	}
	*t = secondPrecision(millis)
	return nil
}

// timestampOrNil converts a nullable column to the pointer a model wants, so a
// NULL stays absent instead of collapsing to the epoch. A row written before
// the wire format was fixed holds sub-second milliseconds; reading it through
// here normalises the value, and the next write persists the normalised one.
func timestampOrNil(v sql.NullInt64) *Timestamp {
	if !v.Valid {
		return nil
	}
	t := secondPrecision(v.Int64)
	return &t
}

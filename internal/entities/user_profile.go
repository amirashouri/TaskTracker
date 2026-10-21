package entities

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// profileColumns is the self-profile projection. It deliberately omits the
// password hash and the timestamps: GET /v1/me has no use for either, and a
// projection that cannot select the hash cannot leak it.
//
// The three nullable columns are coalesced because the initial schema declared
// them without NOT NULL; a row written before the column carried a value still
// answers the profile route instead of failing the scan.
const profileColumns = `id, email, COALESCE(display_name, ''), COALESCE(position, ''),
	COALESCE(avatar_id, ''), presence, presence_note`

// scanProfile reads one row selected with profileColumns.
func scanProfile(row scanner, u *User) error {
	return row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Position, &u.AvatarID, &u.Presence, &u.PresenceNote)
}

// ProfilePatch is the set of self-owned fields a PATCH /v1/me may change.
// A nil pointer means "not in this request", which is why it cannot be
// collapsed into empty strings: clearing a note is a write, omitting it is not.
type ProfilePatch struct {
	DisplayName  *string
	Position     *string
	AvatarID     *string
	Presence     *string
	PresenceNote *string
}

// Empty reports whether the patch asks for no write at all.
func (p ProfilePatch) Empty() bool {
	return p.DisplayName == nil &&
		p.Position == nil &&
		p.AvatarID == nil &&
		p.Presence == nil &&
		p.PresenceNote == nil
}

// GetProfile returns the profile projection for one user, or (nil, nil) when
// the id does not exist.
func (r *UserRepository) GetProfile(ctx context.Context, id UserID) (*User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM users WHERE id = $1`, id)
	return oneOrNil(row, scanProfile)
}

// UpdateProfile writes only the fields present in patch. It is separate from
// Update on purpose: Update rewrites the admin-owned columns (email, status,
// last_seen) from a whole-object read, and letting a presence write share that
// path would either clobber an admin edit or let a stale read resurrect an
// inactive account.
func (r *UserRepository) UpdateProfile(ctx context.Context, id UserID, patch ProfilePatch) error {
	if patch.Empty() {
		return errors.New("profile patch is empty")
	}

	sets := make([]string, 0, 5)
	args := make([]any, 0, 6)
	add := func(column string, value *string) {
		if value == nil {
			return
		}
		args = append(args, *value)
		sets = append(sets, fmt.Sprintf("%s=$%d", column, len(args)))
	}

	add("display_name", patch.DisplayName)
	add("position", patch.Position)
	add("avatar_id", patch.AvatarID)
	add("presence", patch.Presence)
	add("presence_note", patch.PresenceNote)

	args = append(args, id)
	query := `UPDATE users SET ` + strings.Join(sets, ", ") + fmt.Sprintf(` WHERE id=$%d`, len(args))
	return execOneRow(ctx, r.db, errUserNotFound, query, args...)
}

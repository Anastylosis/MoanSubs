package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrInviteInvalid is CreateInvitedAccount's sentinel for "this code does
// not currently redeem" — missing, disabled, expired, and exhausted all
// collapse to the same outcome from a registrant's point of view (WP-C7a
// spec: "0 rows → 403 invite code is not valid").
var ErrInviteInvalid = errors.New("store: invite code is not valid")

// Invite is one registration code (migration 0009): a capability token
// like the session id in migration 0007, not a secret like an account
// token — a member hands the code itself to a friend, so it is stored and
// returned as-is, never hashed.
type Invite struct {
	Code       string
	CreatedBy  int64
	MaxUses    *int // nil = unlimited
	Uses       int
	ExpiresAt  *time.Time
	DisabledAt *time.Time
	CreatedAt  time.Time
}

// InvitedMember is one row of /me's "members you invited" list: just
// enough to show who joined through one of this account's codes and when.
type InvitedMember struct {
	Name     string
	JoinedAt time.Time
}

// InviteWithCreator is one invite plus its creator's name — the CLI's
// node-wide `invite list` (no --for) needs to say who minted each code,
// since the code alone doesn't.
type InviteWithCreator struct {
	Invite
	CreatedByName string
}

// inviteCodeAlphabet drops 0/O/1/l/I (WP-C7a spec: "no look-alikes") so a
// code read aloud or typed by hand doesn't fail on an ambiguous character.
const inviteCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const inviteCodeLen = 12

// generateInviteCode returns a fresh inviteCodeLen-character code drawn
// uniformly from inviteCodeAlphabet. Rejection sampling (rather than a
// plain modulo) keeps every character equally likely — the alphabet's 58
// symbols don't evenly divide 256, so a naive `% len(alphabet)` would
// favor the low end of it.
func generateInviteCode() (string, error) {
	limit := 256 - (256 % len(inviteCodeAlphabet))
	out := make([]byte, 0, inviteCodeLen)
	buf := make([]byte, 1)
	for len(out) < inviteCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= limit {
			continue
		}
		out = append(out, inviteCodeAlphabet[int(buf[0])%len(inviteCodeAlphabet)])
	}
	return string(out), nil
}

// CreateInvite mints an arbitrary invite code attributed to createdBy —
// both the operator's admin-minted codes (CLI `invite create`) and
// EnsureInvites' lazily-minted per-account allotment go through this.
// maxUses nil means unlimited; expiresAt nil means it never expires.
func (s *Store) CreateInvite(ctx context.Context, createdBy int64, maxUses *int, expiresAt *time.Time) (code string, err error) {
	code, err = generateInviteCode()
	if err != nil {
		return "", fmt.Errorf("store: CreateInvite: generating code: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO invites (code, created_by, max_uses, expires_at) VALUES ($1, $2, $3, $4)`,
		code, createdBy, maxUses, expiresAt,
	); err != nil {
		return "", fmt.Errorf("store: CreateInvite: %w", err)
	}
	return code, nil
}

// EnsureInvites tops accountID's own invite allotment up to n single-use
// codes, counting every code it has ever created (used or not) —
// idempotent, so calling it on every /me visit only ever adds the
// shortfall. This is what gives an account created before this package
// shipped its codes too: they arrive the first time its owner visits /me,
// same as a freshly registered one.
func (s *Store) EnsureInvites(ctx context.Context, accountID int64, n int) error {
	var have int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM invites WHERE created_by = $1`, accountID,
	).Scan(&have); err != nil {
		return fmt.Errorf("store: EnsureInvites: counting: %w", err)
	}
	one := 1
	for i := have; i < n; i++ {
		if _, err := s.CreateInvite(ctx, accountID, &one, nil); err != nil {
			return fmt.Errorf("store: EnsureInvites: %w", err)
		}
	}
	return nil
}

// GetInvite returns the invite named by code, or ErrNotFound.
func (s *Store) GetInvite(ctx context.Context, code string) (*Invite, error) {
	var inv Invite
	err := s.pool.QueryRow(ctx, `
		SELECT code, created_by, max_uses, uses, expires_at, disabled_at, created_at
		FROM invites WHERE code = $1`, code,
	).Scan(&inv.Code, &inv.CreatedBy, &inv.MaxUses, &inv.Uses, &inv.ExpiresAt, &inv.DisabledAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetInvite: %w", err)
	}
	return &inv, nil
}

// InvitesByCreator returns every invite accountID has created, newest
// first — the data behind /me's invite table and `invite list --for`.
func (s *Store) InvitesByCreator(ctx context.Context, accountID int64) ([]Invite, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT code, created_by, max_uses, uses, expires_at, disabled_at, created_at
		FROM invites WHERE created_by = $1 ORDER BY created_at DESC, code`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: InvitesByCreator: %w", err)
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.Code, &inv.CreatedBy, &inv.MaxUses, &inv.Uses, &inv.ExpiresAt, &inv.DisabledAt, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: InvitesByCreator: scanning: %w", err)
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: InvitesByCreator: %w", err)
	}
	return out, nil
}

// ListInvitesWithCreators returns every invite on the node, newest first,
// joined to its creator's name — `invite list` with no --for, an
// operator's node-wide view.
func (s *Store) ListInvitesWithCreators(ctx context.Context) ([]InviteWithCreator, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.code, i.created_by, a.name, i.max_uses, i.uses, i.expires_at, i.disabled_at, i.created_at
		FROM invites i JOIN accounts a ON a.id = i.created_by
		ORDER BY i.created_at DESC, i.code`)
	if err != nil {
		return nil, fmt.Errorf("store: ListInvitesWithCreators: %w", err)
	}
	defer rows.Close()

	var out []InviteWithCreator
	for rows.Next() {
		var w InviteWithCreator
		if err := rows.Scan(&w.Code, &w.CreatedBy, &w.CreatedByName, &w.MaxUses, &w.Uses, &w.ExpiresAt, &w.DisabledAt, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: ListInvitesWithCreators: scanning: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListInvitesWithCreators: %w", err)
	}
	return out, nil
}

// DisableInvite marks code disabled. Idempotent — disabling an
// already-disabled code, or one that doesn't exist, is not an error, the
// same "the end state is already true" reasoning as DeleteSession. Callers
// that need to distinguish "no such code" (404) from "already off" call
// GetInvite first, which they need anyway to check who may disable it.
func (s *Store) DisableInvite(ctx context.Context, code string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE invites SET disabled_at = now() WHERE code = $1 AND disabled_at IS NULL`, code,
	); err != nil {
		return fmt.Errorf("store: DisableInvite: %w", err)
	}
	return nil
}

// MembersInvitedBy returns every account whose invited_by is accountID,
// oldest first — /me's "members you invited" list.
func (s *Store) MembersInvitedBy(ctx context.Context, accountID int64) ([]InvitedMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, created_at FROM accounts WHERE invited_by = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: MembersInvitedBy: %w", err)
	}
	defer rows.Close()

	var out []InvitedMember
	for rows.Next() {
		var m InvitedMember
		if err := rows.Scan(&m.Name, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("store: MembersInvitedBy: scanning: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: MembersInvitedBy: %w", err)
	}
	return out, nil
}

// CreateInvitedAccount is CreateAccount's invite-gated variant: redeeming
// the code and creating the account happen in one transaction (WP-C7a
// spec), so a code can never be consumed by a registration that then
// fails on a taken name. The UPDATE's WHERE clause is the whole gate —
// enabled, unexpired, under max_uses — and doing that check as part of
// the row write rather than a separate SELECT is what makes two
// concurrent redemptions of a max_uses=1 code resolve to exactly one
// winner: Postgres serializes the second UPDATE behind the first's
// commit, then re-evaluates the WHERE clause against the
// now-incremented row and finds it no longer qualifies.
func (s *Store) CreateInvitedAccount(ctx context.Context, name, code string) (id int64, token string, invitedBy int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	err = tx.QueryRow(ctx, `
		UPDATE invites SET uses = uses + 1
		WHERE code = $1 AND disabled_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR uses < max_uses)
		RETURNING created_by`, code,
	).Scan(&invitedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", 0, ErrInviteInvalid
	}
	if err != nil {
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: redeeming invite: %w", err)
	}

	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: generating token: %w", err)
	}
	token = hex.EncodeToString(buf)
	tokenHash := HashToken(token)

	if err := tx.QueryRow(ctx,
		`INSERT INTO accounts (name, token_hash, invited_by) VALUES ($1, $2, $3) RETURNING id`,
		name, tokenHash, invitedBy,
	).Scan(&id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, "", 0, ErrNameTaken
		}
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: %w", err)
	}
	return id, token, invitedBy, nil
}

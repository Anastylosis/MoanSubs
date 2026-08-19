package store

import (
	"context"
	"crypto/rand"
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
// both the operator's admin-minted codes (CLI `invite create`) and an
// account's own self-minted code from POST /me/invites (WP-C7c,
// handleCreateInvite) go through this. maxUses nil means unlimited;
// expiresAt nil means it never expires.
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

// InviteBudget computes accountID's invite economy (WP-C7c: invites accrue
// with contribution and hit a cap, replacing the old flat
// MOANSUBS_INVITES_PER_ACCOUNT allotment). initial, perUploads and cap are
// the node's MOANSUBS_INVITES_INITIAL/_PER_UPLOADS/_CAP knobs, passed in
// rather than read from config here because the store package carries no
// server configuration of its own.
//
// earned = initial + floor(uploads / perUploads); perUploads <= 0 means
// earning by upload is disabled, so earned is just initial. minted counts
// every code accountID has ever created, active or not — an admin-minted
// code attributed `--for` this account counts too (CreateInvite doesn't
// distinguish who ran it, and re-deriving "self-minted only" would need a
// column this schema doesn't have; documented in MANUAL.md/SECURITY.md).
// unusedActive is the subset of those still redeemable — enabled,
// unexpired, under max_uses — the same gate createInvitedAccount's UPDATE
// enforces. available is the smaller of "room left under what's been
// earned" and "room left under the cap on codes sitting unused", floored
// at zero: earning more raises the first ceiling, disabling an unused code
// lowers unusedActive and so raises the second.
//
// uploads is also returned since /me's budget line shows it alongside the
// derived numbers (WP-C7c spec: "Uploads counted").
func (s *Store) InviteBudget(ctx context.Context, accountID int64, initial, perUploads, capLimit int) (earned, minted, unusedActive, available, uploads int, err error) {
	err = s.pool.QueryRow(ctx, `
		WITH visible AS (
			SELECT COUNT(*) AS n
			FROM subtitle_tracks t
			JOIN releases r ON r.id = t.release_id
			WHERE t.uploader_id = $1 AND t.withdrawn_at IS NULL AND r.withdrawn_at IS NULL
		),
		minted AS (
			SELECT COUNT(*) AS n FROM invites WHERE created_by = $1
		),
		active AS (
			SELECT COUNT(*) AS n FROM invites
			WHERE created_by = $1 AND disabled_at IS NULL
			  AND (expires_at IS NULL OR expires_at > now())
			  AND (max_uses IS NULL OR uses < max_uses)
		)
		SELECT visible.n, minted.n, active.n FROM visible, minted, active`,
		accountID,
	).Scan(&uploads, &minted, &unusedActive)
	if err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("store: InviteBudget: %w", err)
	}

	earned = initial
	if perUploads > 0 {
		earned += uploads / perUploads
	}
	available = earned - minted
	if room := capLimit - unusedActive; room < available {
		available = room
	}
	if available < 0 {
		available = 0
	}
	return earned, minted, unusedActive, available, uploads, nil
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

// CountPendingInvites returns how many invite codes are currently
// redeemable — enabled, unexpired, under their use limit — /admin index's
// "pending invites" count (WP-C7b). Shares createInvitedAccount's exact
// redemption gate as a plain SELECT so the count can never disagree with
// what actually redeems.
func (s *Store) CountPendingInvites(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM invites
		WHERE disabled_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR uses < max_uses)`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: CountPendingInvites: %w", err)
	}
	return n, nil
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
	id, token, invitedBy, err = s.createInvitedAccount(ctx, name, code, nil)
	if err != nil {
		if errors.Is(err, ErrInviteInvalid) || errors.Is(err, ErrNameTaken) {
			return 0, "", 0, err
		}
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccount: %w", err)
	}
	return id, token, invitedBy, nil
}

// CreateInvitedAccountWithPassword is CreateInvitedAccount's WP-C8 variant:
// the web registration form's invite-mode + password combination, still one
// atomic redeem-then-create transaction. pw must already satisfy
// MinPasswordLen/MaxPasswordLen.
// CreateInvitedAccountWithHash is the precomputed-hash form of
// CreateInvitedAccountWithPassword (see CreateAccountWithHash).
func (s *Store) CreateInvitedAccountWithHash(ctx context.Context, name, code, passwordHash string) (id int64, token string, invitedBy int64, err error) {
	return s.createInvitedAccount(ctx, name, code, &passwordHash)
}

func (s *Store) CreateInvitedAccountWithPassword(ctx context.Context, name, code, pw string) (id int64, token string, invitedBy int64, err error) {
	hash, err := HashPassword(pw)
	if err != nil {
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccountWithPassword: %w", err)
	}
	id, token, invitedBy, err = s.createInvitedAccount(ctx, name, code, &hash)
	if err != nil {
		if errors.Is(err, ErrInviteInvalid) || errors.Is(err, ErrNameTaken) {
			return 0, "", 0, err
		}
		return 0, "", 0, fmt.Errorf("store: CreateInvitedAccountWithPassword: %w", err)
	}
	return id, token, invitedBy, nil
}

// createInvitedAccount is CreateInvitedAccount and
// CreateInvitedAccountWithPassword's shared core — redeeming the code and
// creating the account happen in one transaction (WP-C7a spec), so a code
// can never be consumed by a registration that then fails on a taken name.
// The UPDATE's WHERE clause is the whole redemption gate — enabled,
// unexpired, under max_uses — and doing that check as part of the row
// write rather than a separate SELECT is what makes two concurrent
// redemptions of a max_uses=1 code resolve to exactly one winner: Postgres
// serializes the second UPDATE behind the first's commit, then
// re-evaluates the WHERE clause against the now-incremented row and finds
// it no longer qualifies. passwordHash is nil for the plain (no-password)
// variant, matching createAccount's own nil-means-API-only convention
// (account.go).
func (s *Store) createInvitedAccount(ctx context.Context, name, code string, passwordHash *string) (id int64, token string, invitedBy int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, "", 0, fmt.Errorf("beginning tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	err = tx.QueryRow(ctx, `
		UPDATE invites SET uses = uses + 1
		WHERE code = $1 AND disabled_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses IS NULL OR uses < max_uses)
		  AND created_by NOT IN (SELECT id FROM accounts WHERE disabled)
		RETURNING created_by`, code,
	).Scan(&invitedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", 0, ErrInviteInvalid
	}
	if err != nil {
		return 0, "", 0, fmt.Errorf("redeeming invite: %w", err)
	}

	token, tokenHash, err := generateAccountToken()
	if err != nil {
		return 0, "", 0, err
	}
	tokenEnc, err := s.encryptToken(token)
	if err != nil {
		return 0, "", 0, fmt.Errorf("encrypting token: %w", err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO accounts (name, token_hash, invited_by, password_hash, token_enc) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		name, tokenHash, invitedBy, passwordHash, tokenEnc,
	).Scan(&id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, "", 0, ErrNameTaken
		}
		return 0, "", 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, "", 0, err
	}
	return id, token, invitedBy, nil
}

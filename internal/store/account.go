package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNameTaken is returned by CreateAccount when the name is already in use,
// case-insensitively (migration 0004). Self-registration turns this from an
// operator typo into an ordinary, expected outcome the API answers with 409.
var ErrNameTaken = errors.New("store: account name already taken")

// ErrInvalidCredentials is VerifyAccountPassword's sentinel for "wrong name
// or wrong password" — the two collapse into one outcome and one login
// message (WP-C8 spec: "an unknown name costs the same as a wrong
// password"), so a login attempt can't be used to learn which names are
// registered.
var ErrInvalidCredentials = errors.New("store: invalid name or password")

// ErrNoPassword is VerifyAccountPassword's sentinel for an account that
// exists but has no password set — API-only registration, or a row that
// predates this feature. Distinguished from ErrInvalidCredentials only in
// wording, never in cost: VerifyAccountPassword still runs one PBKDF2 pass
// before returning it.
var ErrNoPassword = errors.New("store: account has no password set")

// Password length bounds (WP-C8): the floor is long enough to resist casual
// guessing without imposing composition rules a stranger can't predict from
// outside; the ceiling is generous but bounded so a password can't be used
// to smuggle an oversized value into the hash function. Registration,
// POST /me/password, and `account set-password` all enforce the same pair,
// from here, so the three can't drift.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

// Account is an upload-authorized identity, created either by the operator
// (`moansubs account create`) or by a visitor registering through
// POST /api/v1/accounts on a node that allows it.
type Account struct {
	ID        int64
	Name      string
	TokenHash string // SHA-256 hex digest; the plaintext token is never stored.
	Disabled  bool
	CreatedAt time.Time
	// Role is one of "user", "mod", "admin" (migration 0009). Every
	// account defaults to "user"; nothing in this package grants mod/admin
	// any privilege yet — that's WP-C7b — but the field is plumbed
	// through now so auth.authResult can carry it.
	Role string
	// TokenEnc is the AES-256-GCM-encrypted token (migration 0010, nonce
	// prefixed), or nil when no MOANSUBS_TOKEN_KEY was configured at
	// mint/rotate time. Pass it to (*Store).DecryptToken to recover the
	// plaintext — /me's own read path for showing the token again after a
	// restart, distinct from the one-shot RotatedToken display.
	TokenEnc []byte
}

// tokenBytes is the size of the random token CreateAccount generates —
// 256 bits, matching PLAN.md "Upload safety": "prints a random 256-bit hex
// token exactly once".
const tokenBytes = 32

// generateAccountToken mints a fresh 256-bit token and its SHA-256 hex
// digest — the pair every account-creating/rotating function below needs,
// factored out so token_enc (WP-C8) gets computed identically everywhere
// rather than risking three slightly different copies of this.
func generateAccountToken() (token, tokenHash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating token: %w", err)
	}
	token = hex.EncodeToString(buf)
	return token, HashToken(token), nil
}

// createAccount is CreateAccount and CreateAccountWithPassword's shared
// core: passwordHash is nil for an API-only account (WP-C8: "without it
// the account is API-only until an admin sets one"), non-nil for a
// registration that supplied one. Also writes token_enc via s.encryptToken
// — NULL when no MOANSUBS_TOKEN_KEY is configured, matching every other
// account-minting method.
func (s *Store) createAccount(ctx context.Context, name string, passwordHash *string) (id int64, token string, err error) {
	token, tokenHash, err := generateAccountToken()
	if err != nil {
		return 0, "", err
	}
	tokenEnc, err := s.encryptToken(token)
	if err != nil {
		return 0, "", fmt.Errorf("encrypting token: %w", err)
	}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO accounts (name, token_hash, password_hash, token_enc) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, tokenHash, passwordHash, tokenEnc,
	).Scan(&id)
	if err != nil {
		// 23505 is unique_violation: either accounts_name_key (exact) or
		// accounts_name_lower_key (case-insensitive, migration 0004).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, "", ErrNameTaken
		}
		return 0, "", err
	}
	return id, token, nil
}

// CreateAccount creates a new account named name and returns its id plus a
// freshly generated 256-bit token, hex-encoded. Only the token's SHA-256 hex
// digest is stored, in accounts.token_hash — the plaintext token returned
// here is the only time it ever exists outside the caller's memory, so
// callers (the `moansubs account create` CLI, and self-registration with no
// password field) must show it to the operator immediately and cannot
// retrieve it again later. The account has no password (password_hash NULL)
// until CreateAccountWithPassword, SetAccountPassword, or `account
// set-password` gives it one.
func (s *Store) CreateAccount(ctx context.Context, name string) (id int64, token string, err error) {
	id, token, err = s.createAccount(ctx, name, nil)
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return 0, "", err
		}
		return 0, "", fmt.Errorf("store: CreateAccount: %w", err)
	}
	return id, token, nil
}

// CreateAccountWithPassword is CreateAccount's WP-C8 variant: self-service
// web registration's actual path now that a browser identity is name +
// password, not just a token. pw must already satisfy MinPasswordLen/
// MaxPasswordLen — this function hashes it (HashPassword) and does not
// re-validate length itself.
func (s *Store) CreateAccountWithPassword(ctx context.Context, name, pw string) (id int64, token string, err error) {
	hash, err := HashPassword(pw)
	if err != nil {
		return 0, "", fmt.Errorf("store: CreateAccountWithPassword: %w", err)
	}
	id, token, err = s.createAccount(ctx, name, &hash)
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return 0, "", err
		}
		return 0, "", fmt.Errorf("store: CreateAccountWithPassword: %w", err)
	}
	return id, token, nil
}

// ListAccounts returns every account, oldest first. The token hash is left
// on the struct but is of no use to a caller — the plaintext is
// unrecoverable by construction.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, token_hash, disabled, created_at, role, token_enc FROM accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: ListAccounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt, &a.Role, &a.TokenEnc); err != nil {
			return nil, fmt.Errorf("store: ListAccounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListAccounts: %w", err)
	}
	return out, nil
}

// AdminAccountRow is one row of /admin/accounts (WP-C7b): everything an
// operator needs to triage an account without opening it individually —
// upload count and inviter name, both resolved via a join, on top of the
// plain account columns.
type AdminAccountRow struct {
	ID            int64
	Name          string
	Role          string
	CreatedAt     time.Time
	Disabled      bool
	UploadCount   int
	InvitedByName *string // nil when not invited (operator-created, or pre-dates invites)
}

// SearchAccounts returns up to limit accounts whose name contains q
// (case-insensitive), newest first — /admin/accounts?q= (WP-C7b). An empty
// q matches every account, so the same query also backs the bare listing.
//
// Deliberately a distinct function from ListAccounts rather than an added
// q/limit pair on it: ListAccounts already has its own signature and
// callers (`moansubs account list`, unfiltered and unlimited) that WP-C7b
// has no business changing — a second, differently-shaped query for a
// second, differently-shaped caller keeps the two from having to agree on
// what their arguments mean.
func (s *Store) SearchAccounts(ctx context.Context, q string, limit int) ([]AdminAccountRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.name, a.role, a.created_at, a.disabled, i.name,
		       (SELECT COUNT(*) FROM subtitle_tracks t WHERE t.uploader_id = a.id)
		FROM accounts a
		LEFT JOIN accounts i ON i.id = a.invited_by
		WHERE a.name ILIKE '%' || $1 || '%'
		ORDER BY a.id DESC
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: SearchAccounts: %w", err)
	}
	defer rows.Close()

	var out []AdminAccountRow
	for rows.Next() {
		var a AdminAccountRow
		var uploads int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.CreatedAt, &a.Disabled, &a.InvitedByName, &uploads); err != nil {
			return nil, fmt.Errorf("store: SearchAccounts: scanning: %w", err)
		}
		a.UploadCount = int(uploads)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: SearchAccounts: %w", err)
	}
	return out, nil
}

// CountAccountsByRole returns the number of accounts holding each role,
// keyed by role — /admin index's per-role counts (WP-C7b).
func (s *Store) CountAccountsByRole(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT role, COUNT(*) FROM accounts GROUP BY role`)
	if err != nil {
		return nil, fmt.Errorf("store: CountAccountsByRole: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int, 3)
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, fmt.Errorf("store: CountAccountsByRole: scanning: %w", err)
		}
		out[role] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: CountAccountsByRole: %w", err)
	}
	return out, nil
}

// SetAccountDisabled flips an account's disabled flag, matched on name
// case-insensitively so an operator revoking access does not have to
// reproduce the registrant's capitalization. Returns ErrNotFound when no
// such account exists.
func (s *Store) SetAccountDisabled(ctx context.Context, name string, disabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET disabled = $2 WHERE lower(name) = lower($1)`, name, disabled)
	if err != nil {
		return fmt.Errorf("store: SetAccountDisabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAccountByName returns the account named name, matched
// case-insensitively like SetAccountDisabled, or ErrNotFound. Needed
// wherever a CLI command works from a name but a store call needs the
// account's id — e.g. `account purge`'s WithdrawTracksByUploader (WP-A1).
func (s *Store) GetAccountByName(ctx context.Context, name string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, token_hash, disabled, created_at, role, token_enc FROM accounts WHERE lower(name) = lower($1)`,
		name,
	).Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt, &a.Role, &a.TokenEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetAccountByName: %w", err)
	}
	return &a, nil
}

// HashToken returns the SHA-256 hex digest of an API token — the only form
// ever persisted or looked up against accounts.token_hash. Exported so the
// API layer's auth middleware and this package's own CreateAccount hash a
// token identically.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GetAccountByTokenHash returns the account whose token_hash matches
// tokenHash, or ErrNotFound if none exists. Does not itself reject disabled
// accounts — callers (the API's Bearer-auth middleware) check Disabled so
// they can log the distinction between "no such token" and "token valid but
// account disabled".
func (s *Store) GetAccountByTokenHash(ctx context.Context, tokenHash string) (*Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, token_hash, disabled, created_at, role, token_enc FROM accounts WHERE token_hash = $1`,
		tokenHash,
	).Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt, &a.Role, &a.TokenEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetAccountByTokenHash: %w", err)
	}
	return &a, nil
}

// RotateAccountToken generates a new token for the account named name
// (case-insensitive), replacing the old token_hash and invalidating the
// old token immediately. Returns the new token (unrecoverable once lost)
// or ErrNotFound if no such account exists. Like CreateAccount, the new
// token must be shown to the account holder exactly once — it is also
// re-encrypted into token_enc (WP-C8), so /me can show it again later too.
func (s *Store) RotateAccountToken(ctx context.Context, name string) (token string, err error) {
	token, tokenHash, err := generateAccountToken()
	if err != nil {
		return "", fmt.Errorf("store: RotateAccountToken: %w", err)
	}
	tokenEnc, err := s.encryptToken(token)
	if err != nil {
		return "", fmt.Errorf("store: RotateAccountToken: encrypting token: %w", err)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET token_hash = $2, token_enc = $3 WHERE lower(name) = lower($1)`,
		name, tokenHash, tokenEnc)
	if err != nil {
		return "", fmt.Errorf("store: RotateAccountToken: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// SetAccountRole sets name's role (case-insensitive match, like
// SetAccountDisabled) — the CLI's `account role NAME ROLE` (WP-C7a). The
// column's CHECK constraint (migration 0009) is the actual gate on what a
// role can be; callers still validate before calling so a typo gets a
// clean CLI error instead of a raw constraint-violation message.
func (s *Store) SetAccountRole(ctx context.Context, name, role string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET role = $2 WHERE lower(name) = lower($1)`, name, role)
	if err != nil {
		return fmt.Errorf("store: SetAccountRole: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// -- Passwords (WP-C8) -------------------------------------------------

// pbkdf2Iterations/pbkdf2SaltBytes/pbkdf2KeyBytes are HashPassword's
// parameters. The iteration count travels inside the encoded hash itself
// (see HashPassword's doc comment) rather than being implied by the code
// version, so raising it later needs no migration — only new hashes get
// the new count, and VerifyPassword reads whatever count a given hash was
// made with.
const (
	pbkdf2Iterations = 600_000
	pbkdf2SaltBytes  = 16
	pbkdf2KeyBytes   = 32
)

// dummyPasswordHash is what an unknown name or a password-less account is
// verified against in VerifyAccountPassword, so a login attempt costs
// exactly one PBKDF2 pass regardless of which of the three outcomes
// (unknown name, no password set, wrong password) it hits — otherwise the
// presence/absence of a real hash to compare against would itself be a
// timing side channel.
var dummyPasswordHash = mustHashPassword("not anybody's password — pbkdf2 filler for constant-time login")

func mustHashPassword(pw string) string {
	h, err := HashPassword(pw)
	if err != nil {
		// Only rand.Read can fail here, at package init time with a fixed
		// input — if that's broken, nothing else in this process works
		// either.
		panic(err)
	}
	return h
}

// HashPassword returns pw's PBKDF2-SHA256 hash (stdlib crypto/pbkdf2, Go
// 1.24+), encoded "pbkdf2-sha256$<iterations>$<base64 salt>$<base64
// hash>" so the parameters travel with the hash and a future iteration
// count change needs no migration to reinterpret already-stored rows.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("store: HashPassword: generating salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, pbkdf2KeyBytes)
	if err != nil {
		return "", fmt.Errorf("store: HashPassword: %w", err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches encoded (HashPassword's own
// output), comparing in constant time. A malformed encoding is always a
// mismatch, never a panic — this parses whatever is actually in
// accounts.password_hash, including a format this build didn't write
// itself.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// VerifyAccountPassword is POST /login's whole check, and POST /me/password's
// re-check of the caller's current password (WP-C8: the web login path is
// name+password now, no token fallback). All three outcomes — unknown
// name, an account with no password set, and a wrong password — run
// exactly one PBKDF2 pass (against dummyPasswordHash when there's no real
// hash to check), so a login attempt cannot be used to enumerate which
// names are registered or which accounts have a password.
func (s *Store) VerifyAccountPassword(ctx context.Context, name, password string) (*Account, error) {
	var a Account
	var hash *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, token_hash, disabled, created_at, role, token_enc, password_hash
		FROM accounts WHERE lower(name) = lower($1)`, name,
	).Scan(&a.ID, &a.Name, &a.TokenHash, &a.Disabled, &a.CreatedAt, &a.Role, &a.TokenEnc, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(dummyPasswordHash, password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("store: VerifyAccountPassword: %w", err)
	}
	if hash == nil {
		VerifyPassword(dummyPasswordHash, password)
		return nil, ErrNoPassword
	}
	if !VerifyPassword(*hash, password) {
		return nil, ErrInvalidCredentials
	}
	return &a, nil
}

// SetAccountPassword sets (or replaces) name's password hash, matched
// case-insensitively like SetAccountDisabled — `account set-password`
// (WP-C8) and POST /me/password's own write. pw must already satisfy
// MinPasswordLen/MaxPasswordLen; this only hashes and stores it.
func (s *Store) SetAccountPassword(ctx context.Context, name, pw string) error {
	hash, err := HashPassword(pw)
	if err != nil {
		return fmt.Errorf("store: SetAccountPassword: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE accounts SET password_hash = $2 WHERE lower(name) = lower($1)`, name, hash)
	if err != nil {
		return fmt.Errorf("store: SetAccountPassword: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// HasAdmin reports whether any account currently holds role "admin" —
// bootstrapAdmin's whole trigger condition (WP-C8: "the trigger is 'no
// admin exists'", not "first run").
func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM accounts WHERE role = 'admin')`,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("store: HasAdmin: %w", err)
	}
	return exists, nil
}

// AccountDetail is `account show`'s data (WP-C8): everything the CLI
// prints about one account, including derived facts (has a password? is
// the token recoverable?) rather than the raw hash/ciphertext columns
// themselves, which a CLI printout has no business displaying.
type AccountDetail struct {
	Name                string
	Role                string
	CreatedAt           time.Time
	Disabled            bool
	InvitedByName       *string // nil when not invited (operator-created, or pre-dates invites)
	HasPassword         bool
	HasDisplayableToken bool // token_enc IS NOT NULL — a key was configured when the token was last minted/rotated
}

// AccountDetail returns name's detail row (case-insensitive match, like
// GetAccountByName), or ErrNotFound.
func (s *Store) AccountDetail(ctx context.Context, name string) (*AccountDetail, error) {
	var d AccountDetail
	err := s.pool.QueryRow(ctx, `
		SELECT a.name, a.role, a.created_at, a.disabled, i.name,
		       a.password_hash IS NOT NULL, a.token_enc IS NOT NULL
		FROM accounts a
		LEFT JOIN accounts i ON i.id = a.invited_by
		WHERE lower(a.name) = lower($1)`, name,
	).Scan(&d.Name, &d.Role, &d.CreatedAt, &d.Disabled, &d.InvitedByName, &d.HasPassword, &d.HasDisplayableToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: AccountDetail: %w", err)
	}
	return &d, nil
}

// -- Token encryption (WP-C8) -------------------------------------------

// tokenNonceBytes is AES-GCM's standard nonce size (96 bits) — using
// cipher.NewGCM's own gcm.NonceSize() at call time rather than hardcoding
// this would be equivalent, but a named constant makes the 12 visible
// without chasing through the stdlib.
const tokenNonceBytes = 12

// encryptToken returns token encrypted under s.tokenKey (AES-256-GCM, a
// random 12-byte nonce prefixed to the ciphertext) for accounts.token_enc,
// or (nil, nil) when no key is configured — every Create*/Rotate* method
// above treats that as "leave token_enc NULL", never as a hard failure:
// a node running without MOANSUBS_TOKEN_KEY must still be able to mint
// accounts, just without a redisplayable token on /me.
func (s *Store) encryptToken(token string) ([]byte, error) {
	if len(s.tokenKey) == 0 {
		return nil, nil
	}
	block, err := aes.NewCipher(s.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("token cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("token GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("token nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, []byte(token), nil), nil
}

// DecryptToken reverses encryptToken — /me's read path for showing the
// plaintext token again on a plain GET, as opposed to RotatedToken's
// one-shot post-rotation display. ok is false whenever the token can't be
// recovered: no key configured, a NULL/short ciphertext, or an
// authentication failure (e.g. MOANSUBS_TOKEN_KEY was rotated since this
// token was minted) — /me shows the same "can't display it" message for
// every one of those, since none of them is something a visitor can act
// on beyond rotating the token.
func (s *Store) DecryptToken(enc []byte) (token string, ok bool) {
	if len(s.tokenKey) == 0 || len(enc) < tokenNonceBytes {
		return "", false
	}
	block, err := aes.NewCipher(s.tokenKey)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}
	nonce, ciphertext := enc[:gcm.NonceSize()], enc[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

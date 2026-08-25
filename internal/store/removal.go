package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type RemovalRequest struct {
	ID            int64
	TrackID       int64
	AccountID     *int64
	FilerName     *string
	Reason        string
	Note          *string
	Contact       *string
	CreatedAt     time.Time
	HandledAt     *time.Time
	HandledBy     *int64
	HandledAction *string
}

func (s *Store) CreateRemovalRequest(ctx context.Context, trackID int64, accountID *int64, reason string, note, contact *string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO removal_requests (track_id, account_id, reason, note, contact)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		trackID, accountID, reason, note, contact,
	).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("store: CreateRemovalRequest: %w", err)
	}
	return id, nil
}

func (s *Store) UnhandledRemovalRequests(ctx context.Context) ([]RemovalRequest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.track_id, r.account_id, a.name, r.reason, r.note, r.contact, r.created_at
		FROM removal_requests r
		LEFT JOIN accounts a ON a.id = r.account_id
		WHERE r.handled_at IS NULL
		ORDER BY r.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: UnhandledRemovalRequests: %w", err)
	}
	defer rows.Close()

	var out []RemovalRequest
	for rows.Next() {
		var req RemovalRequest
		if err := rows.Scan(&req.ID, &req.TrackID, &req.AccountID, &req.FilerName, &req.Reason, &req.Note, &req.Contact, &req.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: UnhandledRemovalRequests: scanning: %w", err)
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: UnhandledRemovalRequests: %w", err)
	}
	return out, nil
}

func (s *Store) GetRemovalRequest(ctx context.Context, id int64) (*RemovalRequest, error) {
	var req RemovalRequest
	err := s.pool.QueryRow(ctx, `
		SELECT id, track_id, account_id, reason, note, contact, created_at, handled_at, handled_by, handled_action
		FROM removal_requests WHERE id = $1`, id,
	).Scan(&req.ID, &req.TrackID, &req.AccountID, &req.Reason, &req.Note, &req.Contact,
		&req.CreatedAt, &req.HandledAt, &req.HandledBy, &req.HandledAction)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: GetRemovalRequest: %w", err)
	}
	return &req, nil
}

// Only an unhandled row is updated, so two moderators racing the same
// request cannot overwrite each other; ErrNotFound covers both cases.
func (s *Store) MarkRemovalRequestHandled(ctx context.Context, id, handledBy int64, action string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE removal_requests SET handled_at = now(), handled_by = $2, handled_action = $3
		WHERE id = $1 AND handled_at IS NULL`, id, handledBy, action)
	if err != nil {
		return fmt.Errorf("store: MarkRemovalRequestHandled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

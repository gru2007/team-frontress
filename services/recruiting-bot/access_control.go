package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

var errCannotRemoveMembers = errors.New("recruiting bot cannot remove testers group members")

func (s *store) ensureAccessSchema(ctx context.Context) error {
	_, err := s.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS key_revocations (
 key_id INTEGER PRIMARY KEY REFERENCES keys(id),
 telegram_id INTEGER NOT NULL UNIQUE REFERENCES users(telegram_id),
 revoked_at INTEGER NOT NULL,
 removed_at INTEGER
);
CREATE INDEX IF NOT EXISTS key_revocations_pending_removal ON key_revocations(removed_at);
`)
	return err
}

func (s *store) keyAccessState(ctx context.Context, id int64) (active, revoked bool, err error) {
	if err = s.ensureAccessSchema(ctx); err != nil {
		return false, false, err
	}
	var keyID int64
	var revokedID sql.NullInt64
	err = s.QueryRowContext(ctx, `SELECT k.id,kr.key_id
FROM keys k LEFT JOIN key_revocations kr ON kr.key_id=k.id
WHERE k.telegram_id=?`, id).Scan(&keyID, &revokedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return !revokedID.Valid, revokedID.Valid, nil
}

func (s *store) hasActiveKey(ctx context.Context, id int64) (bool, error) {
	active, _, err := s.keyAccessState(ctx, id)
	return active, err
}

func (s *store) revokeKey(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureAccessSchema(ctx); err != nil {
		return false, err
	}
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var keyID int64
	err = tx.QueryRowContext(ctx, `SELECT k.id FROM keys k
LEFT JOIN key_revocations kr ON kr.key_id=k.id
WHERE k.telegram_id=? AND kr.key_id IS NULL`, id).Scan(&keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO key_revocations(key_id,telegram_id,revoked_at,removed_at) VALUES(?,?,?,NULL)`, keyID, id, time.Now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *store) restoreKey(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureAccessSchema(ctx); err != nil {
		return false, err
	}
	r, err := s.ExecContext(ctx, `DELETE FROM key_revocations WHERE telegram_id=?`, id)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *store) markGroupRemovalComplete(ctx context.Context, id int64) error {
	if err := s.ensureAccessSchema(ctx); err != nil {
		return err
	}
	_, err := s.ExecContext(ctx, `UPDATE key_revocations SET removed_at=? WHERE telegram_id=?`, time.Now().Unix(), id)
	return err
}

func (s *store) pendingRevokedGroupRemovals(ctx context.Context) ([]int64, error) {
	if err := s.ensureAccessSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.QueryContext(ctx, `SELECT telegram_id FROM key_revocations WHERE removed_at IS NULL ORDER BY revoked_at,telegram_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) legacyUnauthorizedGroupAccess(ctx context.Context) ([]int64, error) {
	if err := s.ensureAccessSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.QueryContext(ctx, `SELECT g.telegram_id FROM group_access g
WHERE NOT EXISTS(
 SELECT 1 FROM keys k LEFT JOIN key_revocations kr ON kr.key_id=k.id
 WHERE k.telegram_id=g.telegram_id AND kr.key_id IS NULL
)
ORDER BY g.telegram_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) clearLegacyGroupAccess(ctx context.Context) error {
	_, err := s.ExecContext(ctx, `DELETE FROM group_access`)
	return err
}

func (a *app) validateRemovalRights(ctx context.Context) error {
	if a.cfg.TestersChatID == 0 {
		return nil
	}
	var bot struct {
		ID int64 `json:"id"`
	}
	if err := a.telegram(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return err
	}
	var member struct {
		Status             string `json:"status"`
		CanRestrictMembers bool   `json:"can_restrict_members"`
	}
	if err := a.telegram(ctx, "getChatMember", map[string]any{"chat_id": a.cfg.TestersChatID, "user_id": bot.ID}, &member); err != nil {
		return err
	}
	if member.Status == "creator" || (member.Status == "administrator" && member.CanRestrictMembers) {
		return nil
	}
	return errCannotRemoveMembers
}

func (a *app) removeFromTestersGroup(ctx context.Context, id int64) error {
	if a.cfg.TestersChatID == 0 {
		return nil
	}
	return a.telegram(ctx, "unbanChatMember", map[string]any{
		"chat_id":        a.cfg.TestersChatID,
		"user_id":        id,
		"only_if_banned": false,
	}, nil)
}

func (a *app) reconcileGroupAccessOnce(ctx context.Context) error {
	if err := a.db.ensureAccessSchema(ctx); err != nil {
		return err
	}
	ids, err := a.db.pendingRevokedGroupRemovals(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := a.removeFromTestersGroup(ctx, id); err != nil {
			return err
		}
		if err := a.db.markGroupRemovalComplete(ctx, id); err != nil {
			return err
		}
	}
	legacy, err := a.db.legacyUnauthorizedGroupAccess(ctx)
	if err != nil {
		return err
	}
	for _, id := range legacy {
		if err := a.removeFromTestersGroup(ctx, id); err != nil {
			return err
		}
	}
	return a.db.clearLegacyGroupAccess(ctx)
}

func (a *app) enforceGroupAccess(ctx context.Context) {
	for {
		if err := a.reconcileGroupAccessOnce(ctx); err != nil && ctx.Err() == nil {
			log.Print("testers group access reconciliation failed; will retry")
		}
		t := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (a *app) revokeTesterKey(ctx context.Context, id int64) (revoked, removed bool, err error) {
	revoked, err = a.db.revokeKey(ctx, id)
	if err != nil || !revoked {
		return revoked, false, err
	}
	if queryID, qerr := a.db.pendingJoinQuery(ctx, id); qerr == nil {
		if a.answerJoinQuery(ctx, queryID, "decline") == nil {
			_ = a.db.clearJoinQuery(ctx, id)
		}
	}
	if err = a.removeFromTestersGroup(ctx, id); err != nil {
		return true, false, err
	}
	if err = a.db.markGroupRemovalComplete(ctx, id); err != nil {
		return true, false, err
	}
	return true, true, nil
}

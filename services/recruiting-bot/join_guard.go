package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

var errJoinRequestExpired = errors.New("join request expired or already processed")
var errGuardBotNotConfigured = errors.New("recruiting bot is not configured as the guard bot")
var errJoinRequestQueriesUnsupported = errors.New("bot does not support join request queries")

func (s *store) rememberJoinQuery(ctx context.Context, id int64, queryID string) error {
	if queryID == "" {
		return errors.New("empty join request query id")
	}
	_, err := s.ExecContext(ctx, `INSERT INTO join_queries(telegram_id,query_id,expires_at) VALUES(?,?,?)
ON CONFLICT(telegram_id) DO UPDATE SET query_id=excluded.query_id,expires_at=excluded.expires_at`, id, queryID, time.Now().Add(24*time.Hour).Unix())
	return err
}

func (s *store) pendingJoinQuery(ctx context.Context, id int64) (string, error) {
	var queryID string
	err := s.QueryRowContext(ctx, `SELECT query_id FROM join_queries WHERE telegram_id=? AND expires_at>?`, id, time.Now().Unix()).Scan(&queryID)
	return queryID, err
}

func (s *store) joinRequestPending(ctx context.Context, id int64) (bool, error) {
	var pending int
	err := s.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM join_queries WHERE telegram_id=? AND expires_at>?)`, id, time.Now().Unix()).Scan(&pending)
	return pending != 0, err
}

func (s *store) clearJoinQuery(ctx context.Context, id int64) error {
	_, err := s.ExecContext(ctx, `DELETE FROM join_queries WHERE telegram_id=?`, id)
	return err
}

func (a *app) answerJoinQuery(ctx context.Context, queryID, result string) error {
	return a.telegram(ctx, "answerChatJoinRequestQuery", map[string]any{
		"chat_join_request_query_id": queryID,
		"result":                     result,
	}, nil)
}

func (a *app) resolvePendingJoinQuery(ctx context.Context, id int64) error {
	queryID, err := a.db.pendingJoinQuery(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errJoinRequestExpired
	}
	if err != nil {
		return err
	}
	err = a.answerJoinQuery(ctx, queryID, "approve")
	var te *telegramError
	if errors.As(err, &te) && te.Code == 400 {
		_ = a.db.clearJoinQuery(ctx, id)
		return errJoinRequestExpired
	}
	if err != nil {
		return err
	}
	return a.db.clearJoinQuery(ctx, id)
}

func (a *app) approveJoinFromMiniApp(w http.ResponseWriter, r *http.Request, id int64) {
	// Keep accepting the old field for already-open Guard Mini Apps, but it no longer grants access.
	var body struct {
		GrantGroupAccess bool `json:"grant_group_access"`
	}
	if !decode(w, r, &body) {
		return
	}
	if a.cfg.TestersChatID == 0 {
		fail(w, http.StatusConflict, "Группа тестеров пока не настроена.")
		return
	}
	pending, err := a.db.joinRequestPending(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	if !pending {
		fail(w, http.StatusConflict, "Заявка уже обработана или истекла. Подайте её заново.")
		return
	}
	eligible, err := a.db.hasActiveKey(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	if !eligible {
		fail(w, http.StatusForbidden, "Для вступления в группу сначала получите активный ключ Team Frontress.")
		return
	}
	if err := a.resolvePendingJoinQuery(r.Context(), id); err != nil {
		if errors.Is(err, errJoinRequestExpired) {
			fail(w, http.StatusConflict, "Заявка уже обработана или истекла. Подайте её заново.")
			return
		}
		fail(w, http.StatusBadGateway, "Не удалось подтвердить заявку в Telegram. Попробуйте ещё раз.")
		return
	}
	respond(w, map[string]bool{"approved": true})
}

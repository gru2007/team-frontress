package main

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

func (s *store) ensureReferralSchema(ctx context.Context) error {
	_, err := s.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS referrals (
 referee_id INTEGER PRIMARY KEY REFERENCES users(telegram_id),
 referrer_id INTEGER NOT NULL REFERENCES users(telegram_id),
 created_at INTEGER NOT NULL,
 CHECK(referee_id <> referrer_id)
);
CREATE INDEX IF NOT EXISTS referrals_referrer ON referrals(referrer_id);
`)
	return err
}

func (s *store) botStarted(ctx context.Context, id int64) (bool, error) {
	var started int
	err := s.QueryRowContext(ctx, `SELECT bot_started FROM users WHERE telegram_id=?`, id).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return started != 0, err
}

func parseReferralPayload(payload string) (int64, bool) {
	if !strings.HasPrefix(payload, "ref_") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(payload, "ref_"), 10, 64)
	return id, err == nil && id > 0
}

func (s *store) recordReferral(ctx context.Context, refereeID, referrerID int64) (bool, error) {
	if refereeID <= 0 || referrerID <= 0 || refereeID == referrerID {
		return false, nil
	}
	if err := s.ensureReferralSchema(ctx); err != nil {
		return false, err
	}
	var exists int
	if err := s.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE telegram_id=?)`, referrerID).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	r, err := s.ExecContext(ctx, `INSERT OR IGNORE INTO referrals(referee_id,referrer_id,created_at) VALUES(?,?,unixepoch())`, refereeID, referrerID)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

func (s *store) referralCount(ctx context.Context, id int64) (int, error) {
	if err := s.ensureReferralSchema(ctx); err != nil {
		return 0, err
	}
	var count int
	err := s.QueryRowContext(ctx, `SELECT COUNT(*) FROM referrals WHERE referrer_id=?`, id).Scan(&count)
	return count, err
}

func (a *app) referralLink(ctx context.Context, id int64) (string, error) {
	var bot struct {
		Username string `json:"username"`
	}
	if err := a.telegram(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return "", err
	}
	if bot.Username == "" || strings.ContainsAny(bot.Username, "/?&#") {
		return "", errors.New("bot username is unavailable")
	}
	return "https://t.me/" + bot.Username + "?start=ref_" + strconv.FormatInt(id, 10), nil
}

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type telegramError struct {
	Code       int
	RetryAfter int
}

func (e *telegramError) Error() string {
	return "Telegram request failed (" + strconv.Itoa(e.Code) + ")"
}

func permanentTelegramError(err error) bool {
	var te *telegramError
	return errors.As(err, &te) && te.Code >= 400 && te.Code < 500 && te.Code != 429
}

func (a *app) telegram(ctx context.Context, method string, body any, result any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.telegramBase+"/bot"+a.cfg.BotToken+"/"+method, bytes.NewReader(b))
	if err != nil {
		return errors.New("cannot construct Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return errors.New("Telegram transport failed")
	}
	defer res.Body.Close()
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 {
		return errors.New("Telegram response too large or unreadable")
	}
	if err = json.Unmarshal(b, &envelope); err != nil {
		return safeError(res.StatusCode)
	}
	if !envelope.OK || res.StatusCode != 200 {
		code := envelope.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &telegramError{Code: code, RetryAfter: envelope.Parameters.RetryAfter}
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func retryDelay(err error, attempt int) time.Duration {
	var te *telegramError
	if errors.As(err, &te) && te.Code == 429 && te.RetryAfter > 0 {
		seconds := int64(te.RetryAfter)
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds+1) * time.Second
	}
	if attempt > 10 {
		attempt = 10
	}
	if attempt < 0 {
		attempt = 0
	}
	return time.Duration(1<<attempt) * time.Second
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		From telegramUser `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
	Join *struct {
		From telegramUser `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		QueryID string `json:"query_id"`
	} `json:"chat_join_request"`
}

func (a *app) poll(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		var offset int64
		if err := a.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE name='offset'`).Scan(&offset); err != nil {
			if !sleep(ctx, time.Second) {
				return
			}
			continue
		}
		var updates []update
		err := a.telegram(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "limit": 50, "allowed_updates": []string{"message", "chat_join_request"}}, &updates)
		if err != nil {
			attempt++
			if ctx.Err() == nil {
				log.Print("Telegram polling failed; retrying")
			}
			if !sleep(ctx, retryDelay(err, attempt)) {
				return
			}
			continue
		}
		attempt = 0
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			if err := a.handleUpdate(ctx, u); err != nil && !permanentTelegramError(err) {
				log.Print("Telegram update failed; retrying")
				if !sleep(ctx, retryDelay(err, 1)) {
					return
				}
				break
			}
			if _, err := a.db.ExecContext(ctx, `UPDATE settings SET value=? WHERE name='offset'`, u.ID+1); err != nil {
				break
			}
			offset = u.ID + 1
		}
	}
}

func (a *app) handleUpdate(ctx context.Context, u update) error {
	if u.Join != nil {
		j := u.Join
		if a.cfg.TestersChatID == 0 || j.Chat.ID != a.cfg.TestersChatID || j.From.ID <= 0 || j.From.IsBot {
			return nil
		}
		if err := a.db.register(ctx, j.From.ID, j.From.FirstName, false); err != nil {
			return err
		}
		eligible, err := a.db.hasActiveKey(ctx, j.From.ID)
		if err != nil {
			return err
		}
		if j.QueryID != "" {
			if eligible {
				err = a.answerJoinQuery(ctx, j.QueryID, "approve")
			} else {
				if err = a.db.rememberJoinQuery(ctx, j.From.ID, j.QueryID); err != nil {
					return err
				}
				err = a.telegram(ctx, "sendChatJoinRequestWebApp", map[string]any{
					"chat_join_request_query_id": j.QueryID,
					"web_app_url":                a.cfg.PublicURL + "/?join_request=1",
				}, nil)
				if err != nil {
					_ = a.db.clearJoinQuery(ctx, j.From.ID)
				}
			}
			var te *telegramError
			if errors.As(err, &te) && te.Code == 400 {
				return nil
			}
			return err
		}
		method := "declineChatJoinRequest"
		if eligible {
			method = "approveChatJoinRequest"
		}
		err = a.telegram(ctx, method, map[string]any{"chat_id": j.Chat.ID, "user_id": j.From.ID}, nil)
		var te *telegramError
		if errors.As(err, &te) && te.Code == 400 {
			return nil
		}
		return err
	}
	if u.Message == nil || u.Message.Chat.Type != "private" || u.Message.From.ID <= 0 || u.Message.From.IsBot || u.Message.Chat.ID != u.Message.From.ID {
		return nil
	}
	m := u.Message
	words := strings.Fields(m.Text)
	command := ""
	if len(words) > 0 {
		command = strings.Split(words[0], "@")[0]
	}
	firstBotStart := false
	if command == "/start" {
		started, err := a.db.botStarted(ctx, m.From.ID)
		if err != nil {
			return err
		}
		firstBotStart = !started
	}
	if err := a.db.register(ctx, m.From.ID, m.From.FirstName, true); err != nil {
		return err
	}
	if firstBotStart && len(words) > 1 {
		if referrerID, ok := parseReferralPayload(words[1]); ok {
			if _, err := a.db.recordReferral(ctx, m.From.ID, referrerID); err != nil {
				return err
			}
		}
	}
	if len(words) == 0 {
		return nil
	}
	text := ""
	var markup any
	switch command {
	case "/start", "/help":
		text = "Team Frontress набирает тестеров! Откройте приложение, привяжите Steam и получите ключ. Если ключи закончились, вы останетесь в списке ожидания: проверьте /key позже.\n\n/start — начать\n/help — помощь\n/key — мой ключ\n/group — ссылка в группу тестеров\n/ref — моя реферальная ссылка\n\nВ группу допускаются только участники с активным выданным ключом. Если подать заявку напрямую без ключа, Telegram откроет Mini App, но заявка будет одобрена только после получения ключа. При отзыве ключа доступ прекращается и участник удаляется из группы. Привязка Steam постоянная. Мы не запрашиваем пароль Steam."
		markup = map[string]any{"inline_keyboard": [][]any{{map[string]any{"text": "Открыть Team Frontress", "web_app": map[string]string{"url": a.cfg.PublicURL + "/"}}}}}
	case "/key":
		_, revoked, err := a.db.keyAccessState(ctx, m.From.ID)
		if err != nil {
			return err
		}
		if revoked {
			text = "Ваш ключ Team Frontress отозван администратором. Доступ в группу тестеров отключён."
			break
		}
		key, err := a.db.claim(ctx, m.From.ID)
		if errors.Is(err, errNotLinked) {
			text = "Сначала откройте приложение через /start и привяжите Steam."
		} else if err != nil {
			return err
		} else if key == "" {
			text = "Свободные ключи закончились. Вы в списке ожидания; проверьте /key позже."
		} else {
			text = "Ваш ключ Team Frontress:\n" + key + "\n\nНе передавайте ключ другим."
		}
	case "/group":
		if a.cfg.TestersChatID == 0 {
			text = "Группа тестеров пока не настроена."
			break
		}
		eligible, err := a.db.hasActiveKey(ctx, m.From.ID)
		if err != nil {
			return err
		}
		if !eligible {
			text = "Доступ в группу открывается только после получения активного ключа Team Frontress. Откройте Mini App или используйте /key после привязки Steam."
			break
		}
		link, err := a.groupEntry(ctx)
		if err != nil {
			if !permanentTelegramError(err) {
				return err
			}
			text = "Не удалось создать приглашение в группу. Обратитесь к организаторам или попробуйте позже."
		} else {
			text = "Подайте заявку на вступление в группу тестеров — бот проверит ваш активный ключ и одобрит её:\n" + link
		}
	case "/ref":
		count, err := a.db.referralCount(ctx, m.From.ID)
		if err != nil {
			return err
		}
		link, err := a.referralLink(ctx, m.From.ID)
		if err != nil {
			return err
		}
		text = "Ваша реферальная ссылка Team Frontress:\n" + link + "\n\nПриглашено: " + strconv.Itoa(count) + "\n\nРеферал засчитывается при первом запуске бота по вашей ссылке."
	default:
		text = "Используйте /start, /help, /key, /group или /ref."
	}
	body := map[string]any{"chat_id": m.Chat.ID, "text": text}
	if markup != nil {
		body["reply_markup"] = markup
	}
	err := a.telegram(ctx, "sendMessage", body, nil)
	var te *telegramError
	if errors.As(err, &te) && te.Code == 403 {
		_, err = a.db.ExecContext(ctx, `UPDATE users SET blocked=1 WHERE telegram_id=?`, m.From.ID)
	}
	return err
}

var errPublicGroupJoinRequestsDisabled = errors.New("public testers group does not require join requests")

type testersGroupInfo struct {
	Type          string        `json:"type"`
	Username      string        `json:"username"`
	JoinByRequest bool          `json:"join_by_request"`
	GuardBot      *telegramUser `json:"guard_bot"`
}

func (a *app) testersGroup(ctx context.Context) (testersGroupInfo, error) {
	var result testersGroupInfo
	if a.cfg.TestersChatID == 0 {
		return result, errors.New("testers group is disabled")
	}
	err := a.telegram(ctx, "getChat", map[string]any{"chat_id": a.cfg.TestersChatID}, &result)
	if err != nil {
		return result, err
	}
	if result.Type != "group" && result.Type != "supergroup" {
		return result, errors.New("TESTERS_CHAT_ID is not a group")
	}
	return result, nil
}

func (a *app) validateGroup(ctx context.Context) error {
	if a.cfg.TestersChatID == 0 {
		return nil
	}
	group, err := a.testersGroup(ctx)
	if err != nil {
		return err
	}
	if group.Username != "" && !group.JoinByRequest {
		return errPublicGroupJoinRequestsDisabled
	}
	var bot struct {
		ID                         int64 `json:"id"`
		SupportsJoinRequestQueries bool  `json:"supports_join_request_queries"`
	}
	if err := a.telegram(ctx, "getMe", map[string]any{}, &bot); err != nil {
		return err
	}
	if !bot.SupportsJoinRequestQueries {
		return errJoinRequestQueriesUnsupported
	}
	if group.GuardBot == nil || group.GuardBot.ID != bot.ID {
		return errGuardBotNotConfigured
	}
	return nil
}

func (a *app) groupEntry(ctx context.Context) (string, error) {
	return a.groupInvite(ctx)
}

func (a *app) groupInvite(ctx context.Context) (string, error) {
	var result struct {
		InviteLink string `json:"invite_link"`
	}
	err := a.telegram(ctx, "createChatInviteLink", map[string]any{"chat_id": a.cfg.TestersChatID, "creates_join_request": true, "expire_date": time.Now().Add(time.Hour).Unix(), "name": "Team Frontress testers"}, &result)
	if err == nil && !strings.HasPrefix(result.InviteLink, "https://t.me/") {
		err = errors.New("invalid invitation")
	}
	return result.InviteLink, err
}

func (a *app) deliver(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.deliverOne(ctx); errors.Is(err, sql.ErrNoRows) {
			if !sleep(ctx, time.Second) {
				return
			}
		} else if err != nil {
			if ctx.Err() == nil {
				log.Print("announcement delivery failed; will retry")
			}
			if !sleep(ctx, time.Second) {
				return
			}
		} else if !sleep(ctx, 100*time.Millisecond) {
			return
		}
	}
}

func (a *app) deliverOne(ctx context.Context) error {
	// Lease before network I/O; a crashed process makes the delivery retryable.
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cooldown int64
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE name='delivery_cooldown'`).Scan(&cooldown); err != nil {
		return err
	}
	if cooldown > time.Now().Unix() {
		return sql.ErrNoRows
	}
	var id, userID int64
	var attempts int
	var text string
	err = tx.QueryRowContext(ctx, `SELECT d.announcement_id,d.telegram_id,d.attempts,a.text FROM deliveries d JOIN announcements a ON a.id=d.announcement_id JOIN users u ON u.telegram_id=d.telegram_id WHERE d.done=0 AND d.next_at<=? AND u.blocked=0 ORDER BY d.next_at,d.announcement_id,d.telegram_id LIMIT 1`, time.Now().Unix()).Scan(&id, &userID, &attempts, &text)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE deliveries SET attempts=attempts+1,next_at=? WHERE announcement_id=? AND telegram_id=?`, time.Now().Add(2*time.Minute).Unix(), id, userID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	err = a.telegram(ctx, "sendMessage", map[string]any{"chat_id": userID, "text": text}, nil)
	done := err == nil
	var te *telegramError
	blocked := errors.As(err, &te) && te.Code == 403
	if blocked || (te != nil && te.Code == 400) {
		done = true
	}
	next := time.Now().Add(retryDelay(err, attempts+1)).Unix()
	tx, dbErr := a.db.BeginTx(ctx, nil)
	if dbErr != nil {
		return dbErr
	}
	defer tx.Rollback()
	if blocked {
		if _, dbErr = tx.ExecContext(ctx, `UPDATE users SET blocked=1 WHERE telegram_id=?`, userID); dbErr != nil {
			return dbErr
		}
		if _, dbErr = tx.ExecContext(ctx, `UPDATE deliveries SET done=1 WHERE telegram_id=?`, userID); dbErr != nil {
			return dbErr
		}
	}
	if _, dbErr = tx.ExecContext(ctx, `UPDATE deliveries SET done=?,next_at=? WHERE announcement_id=? AND telegram_id=?`, done, next, id, userID); dbErr != nil {
		return dbErr
	}
	if te != nil && te.Code == 429 {
		if _, dbErr = tx.ExecContext(ctx, `UPDATE settings SET value=MAX(value,?) WHERE name='delivery_cooldown'`, next); dbErr != nil {
			return dbErr
		}
		if _, dbErr = tx.ExecContext(ctx, `UPDATE deliveries SET next_at=MAX(next_at,?) WHERE done=0`, next); dbErr != nil {
			return dbErr
		}
	}
	return tx.Commit()
}

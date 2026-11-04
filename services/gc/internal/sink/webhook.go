// Package sink tells other community projects what happened: a match started,
// a match ended and how. A pickup site, a stats page or a Discord bot
// subscribes with a URL and gets JSON.
//
// Delivery is asynchronous and retried with backoff. A hook that is down
// costs that hook its events, never anybody's match: the GC does not wait
// for it and does not remember more than a bounded queue of undelivered
// events.
package sink

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/config"
)

// Event is one thing that happened.
type Event struct {
	Type string    `json:"type"` // "match_started", "match_ended"
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// Webhooks delivers events to every configured hook.
type Webhooks struct {
	hooks []config.WebhookConfig
	log   *slog.Logger
	queue chan delivery
	http  *http.Client
}

type delivery struct {
	hook  config.WebhookConfig
	body  []byte
	tries int
}

const (
	queueSize  = 1024
	maxTries   = 8
	firstRetry = 5 * time.Second
)

// New makes a sink. With no hooks it accepts and drops everything.
func New(hooks []config.WebhookConfig, log *slog.Logger) *Webhooks {
	return &Webhooks{
		hooks: hooks,
		log:   log,
		queue: make(chan delivery, queueSize),
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Run delivers until ctx ends.
func (w *Webhooks) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-w.queue:
			if err := w.post(ctx, d); err != nil {
				d.tries++
				if d.tries >= maxTries {
					w.log.Warn("webhook: giving up", "hook", d.hook.Name, "err", err)
					continue
				}
				wait := firstRetry << (d.tries - 1)
				w.log.Info("webhook: will retry", "hook", d.hook.Name, "in", wait, "err", err)
				go func(d delivery) {
					select {
					case <-time.After(wait):
						w.enqueue(d)
					case <-ctx.Done():
					}
				}(d)
			}
		}
	}
}

// Emit queues an event for every hook that wants it.
func (w *Webhooks) Emit(e Event) {
	if len(w.hooks) == 0 {
		return
	}
	body, err := json.Marshal(e)
	if err != nil {
		return
	}
	for _, h := range w.hooks {
		if !wants(h, e.Type) {
			continue
		}
		w.enqueue(delivery{hook: h, body: body})
	}
}

func (w *Webhooks) enqueue(d delivery) {
	select {
	case w.queue <- d:
	default:
		w.log.Warn("webhook: queue full, dropping event", "hook", d.hook.Name)
	}
}

func wants(h config.WebhookConfig, typ string) bool {
	if len(h.Events) == 0 {
		return true
	}
	for _, e := range h.Events {
		if e == typ {
			return true
		}
	}
	return false
}

func (w *Webhooks) post(ctx context.Context, d delivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.hook.URL, bytes.NewReader(d.body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.hook.Secret != "" {
		mac := hmac.New(sha256.New, []byte(d.hook.Secret))
		mac.Write(d.body)
		req.Header.Set("X-Frontress-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

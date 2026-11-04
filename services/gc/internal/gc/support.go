package gc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/support"
)

// The support tab: donations and a monthly subscription through Steam (see
// package support for the flow). Steam is slow and remote, so every call to
// it runs off the loop and posts its result back.

const steamTimeout = 25 * time.Second

func (g *GC) supportOn() bool { return g.cfg.Support.Enabled && g.steam != nil && g.book != nil }

func (g *GC) handleSupport(id uint64, op socialOp) {
	if !g.supportOn() {
		g.emit(id, "support", map[string]any{"available": false})
		return
	}
	switch op.Op {
	case "support_info":
		g.supportInfo(id)
	case "support_buy":
		g.supportBuy(id, support.Kind(op.Kind), op.Tier, op.Anon)
	case "support_auth":
		order, _ := strconv.ParseUint(op.Order, 10, 64)
		g.supportAuthorized(id, order, op.OK)
	case "support_cancel":
		g.supportCancel(id)
	}
}

// offSteam runs a Steam call off the loop and its result back on it.
func (g *GC) offSteam(call func(ctx context.Context) error, then func(error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), steamTimeout)
		defer cancel()
		err := call(ctx)
		g.Do(func() { then(err) })
	}()
}

// sendSupportSummary is the small part every menu shows: is this player a
// supporter.
func (g *GC) sendSupportSummary(id uint64) {
	if g.book == nil {
		return
	}
	sup, monthly := g.book.IsSupporter(id)
	g.emit(id, "supporter", map[string]any{"supporter": sup, "monthly": monthly, "available": g.supportOn()})
}

// supportInfo sends the support tab: prices in the player's currency, their
// status, the thank-you wall.
func (g *GC) supportInfo(id uint64) {
	var currency string
	var agreements []support.Agreement
	g.offSteam(func(ctx context.Context) error {
		var err error
		if currency, err = g.steam.UserCurrency(ctx, id); err != nil {
			return err
		}
		agreements, _ = g.steam.Agreements(ctx, id)
		return nil
	}, func(err error) {
		if err != nil {
			g.log.Warn("support: user info", "steamid", id, "err", err)
			g.emit(id, "support", g.supportDoc(id, "USD", false, steamErrKey(err)))
			return
		}
		active := false
		for _, a := range agreements {
			active = active || strings.EqualFold(a.Status, "Active")
		}
		g.book.SetMonthly(id, active)
		if p := g.online[id]; p != nil {
			p.Currency = currency
		}
		g.emit(id, "support", g.supportDoc(id, currency, true, ""))
	})
}

func (g *GC) supportDoc(id uint64, currency string, ok bool, problem string) map[string]any {
	currency, prices := support.PricesFor(currency, g.supportPrices())
	tiers := func(kind support.Kind, list []int64) []map[string]any {
		out := []map[string]any{}
		for i, a := range list {
			out = append(out, map[string]any{"tier": i, "kind": string(kind), "amount": a, "price": FormatPrice(a, currency)})
		}
		return out
	}
	me := map[string]any{"supporter": false, "monthly": false, "donations": 0, "since": 0}
	if s := g.book.Supporter(id); s != nil {
		me = map[string]any{"supporter": true, "monthly": s.Monthly, "donations": s.Donations, "since": s.Since.Unix()}
	}
	wall := []map[string]any{}
	for _, t := range g.book.Wall(16) {
		wall = append(wall, map[string]any{"name": t.Name, "kind": string(t.Kind), "ago": int(g.now().Sub(t.At).Seconds())})
	}
	return map[string]any{
		"available": ok, "problem": problem, "sandbox": g.cfg.Support.Sandbox || g.cfg.Support.Fake,
		"currency": currency, "once": tiers(support.Once, prices.Once), "monthly": tiers(support.Monthly, prices.Monthly),
		"me": me, "wall": wall, "count": g.book.Count(), "subscribers": g.book.Subscribers(),
	}
}

func (g *GC) supportPrices() map[string]support.Prices {
	out := map[string]support.Prices{}
	for k, v := range g.cfg.Support.Prices {
		out[strings.ToUpper(k)] = support.Prices{Once: v.Once, Monthly: v.Monthly}
	}
	return out
}

// steamErrKey turns a Steam failure into something the game can explain.
func steamErrKey(err error) string {
	var se *support.SteamError
	if errors.As(err, &se) {
		switch se.Code {
		case "locked":
			return "locked"
		case "8":
			return "currency"
		case "10":
			return "declined"
		case "11":
			return "country"
		case "16":
			return "already_subscribed"
		case "100":
			return "funds"
		}
		return "steam"
	}
	return "unreachable"
}

// supportBuy opens an order. Steam shows the player the approval dialog; the
// game tells us how it went (supportAuthorized).
func (g *GC) supportBuy(id uint64, kind support.Kind, tier int, anon bool) {
	if kind != support.Once && kind != support.Monthly {
		return
	}
	p := g.player(id)
	if !p.supportBusy.IsZero() && g.now().Sub(p.supportBusy) < time.Minute {
		g.fail(id, "support_busy")
		return
	}
	p.supportBusy = g.now()
	name := g.nameOf(id)
	lang := langCode(p.Lang)
	var order support.Order
	g.offSteam(func(ctx context.Context) error {
		currency, err := g.steam.UserCurrency(ctx, id)
		if err != nil {
			return err
		}
		currency, prices := support.PricesFor(currency, g.supportPrices())
		amount, err := prices.Amount(kind, tier)
		if err != nil {
			return err
		}
		order = support.Order{ID: newID(), SteamID: id, Name: name, Kind: kind, Tier: tier, Amount: amount, Currency: currency, Anon: anon}
		g.book.Open(order)
		desc := fmt.Sprintf("Team Frontress: donation %s", FormatPrice(amount, currency))
		if kind == support.Monthly {
			desc = fmt.Sprintf("Team Frontress: monthly support %s", FormatPrice(amount, currency))
		}
		return g.steam.InitTxn(ctx, support.Txn{OrderID: order.ID, SteamID: id, Language: lang, Currency: currency,
			Amount: amount, ItemID: support.ItemID(kind, tier), Description: desc, Monthly: kind == support.Monthly})
	}, func(err error) {
		if err != nil {
			p.supportBusy = time.Time{}
			if order.ID != 0 {
				g.book.Failed(order.ID, err.Error())
			}
			g.log.Warn("support: InitTxn", "steamid", id, "err", err)
			g.emit(id, "support_result", map[string]any{"ok": false, "error": steamErrKey(err)})
			return
		}
		g.log.Info("support: order opened", "steamid", id, "order", order.ID, "kind", kind, "amount", order.Amount, "currency", order.Currency)
		if g.cfg.Support.Fake {
			// No overlay to approve in: approve it ourselves.
			g.supportAuthorized(id, order.ID, true)
			return
		}
		g.emit(id, "support_pending", map[string]any{"order": strconv.FormatUint(order.ID, 10)})
	})
}

// supportAuthorized is the game passing on Steam's answer
// (MicroTxnAuthorizationResponse_t). Approved orders are finalized: then,
// and only then, are they paid.
func (g *GC) supportAuthorized(id, orderID uint64, ok bool) {
	o := g.book.Order(orderID)
	if o == nil || o.SteamID != id || !o.Paid.IsZero() || o.Failed != "" {
		return
	}
	p := g.player(id)
	if !ok {
		p.supportBusy = time.Time{}
		g.book.Failed(orderID, "declined")
		g.emit(id, "support_result", map[string]any{"ok": false, "error": "declined"})
		return
	}
	g.offSteam(func(ctx context.Context) error {
		return g.steam.FinalizeTxn(ctx, orderID)
	}, func(err error) {
		p.supportBusy = time.Time{}
		if err != nil {
			g.book.Failed(orderID, err.Error())
			g.log.Warn("support: FinalizeTxn", "steamid", id, "order", orderID, "err", err)
			g.emit(id, "support_result", map[string]any{"ok": false, "error": steamErrKey(err)})
			return
		}
		if !g.book.Paid(orderID) {
			return
		}
		g.log.Info("support: paid", "steamid", id, "order", orderID, "kind", o.Kind, "amount", o.Amount, "currency", o.Currency)
		g.emit(id, "support_result", map[string]any{"ok": true, "kind": string(o.Kind)})
		if !o.Anon {
			key := "fui_feed_support"
			if o.Kind == support.Monthly {
				key = "fui_feed_support_monthly"
			}
			g.feed("support", key, o.Name)
		}
		g.sendSupportSummary(id)
		g.emit(id, "support", g.supportDoc(id, o.Currency, true, ""))
		g.presenceLast = ""
	})
}

// supportCancel stops the player's subscription.
func (g *GC) supportCancel(id uint64) {
	g.offSteam(func(ctx context.Context) error {
		agreements, err := g.steam.Agreements(ctx, id)
		if err != nil {
			return err
		}
		for _, a := range agreements {
			if strings.EqualFold(a.Status, "Active") {
				if err := g.steam.CancelAgreement(ctx, id, a.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}, func(err error) {
		if err != nil {
			g.emit(id, "support_result", map[string]any{"ok": false, "error": steamErrKey(err)})
			return
		}
		g.book.SetMonthly(id, false)
		g.emit(id, "support_result", map[string]any{"ok": true, "kind": "cancelled"})
		g.sendSupportSummary(id)
		g.supportInfo(id)
	})
}

// langCode is the ISO 639-1 code for a Steam language name.
func langCode(lang string) string {
	switch lang {
	case "russian":
		return "ru"
	case "ukrainian":
		return "uk"
	case "german":
		return "de"
	case "french":
		return "fr"
	case "spanish", "latam":
		return "es"
	case "polish":
		return "pl"
	case "brazilian", "portuguese":
		return "pt"
	case "schinese", "tchinese":
		return "zh"
	}
	return "en"
}

// FormatPrice writes an amount in cents the way its currency is written.
func FormatPrice(cents int64, currency string) string {
	whole, frac := cents/100, cents%100
	num := strconv.FormatInt(whole, 10)
	if len(num) > 3 {
		var b strings.Builder
		for i, r := range num {
			if i > 0 && (len(num)-i)%3 == 0 {
				b.WriteRune(' ')
			}
			b.WriteRune(r)
		}
		num = b.String()
	}
	dec := func(sep string) string {
		if frac == 0 {
			return num
		}
		return fmt.Sprintf("%s%s%02d", num, sep, frac)
	}
	switch currency {
	case "USD":
		return "$" + dec(".")
	case "EUR":
		return dec(",") + " €"
	case "GBP":
		return "£" + dec(".")
	case "RUB":
		return num + " ₽"
	case "UAH":
		return num + " ₴"
	case "KZT":
		return num + " ₸"
	case "PLN":
		return dec(",") + " zł"
	case "BRL":
		return "R$ " + dec(",")
	case "CNY":
		return "¥" + dec(".")
	}
	return dec(".") + " " + currency
}

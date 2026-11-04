package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/gc/internal/gc"
	"github.com/gru2007/team-frontress/services/gc/internal/gcpb"
	"github.com/gru2007/team-frontress/services/gc/internal/httpapi"
	"github.com/gru2007/team-frontress/services/gc/internal/steamauth"
	"github.com/gru2007/team-frontress/services/gc/internal/war"
	"google.golang.org/protobuf/proto"
)

// The war screen reads /v1/war as it is; check it has what the screen uses.
func TestWarView(t *testing.T) {
	theater, err := war.LoadTheater("../../theater.example.json")
	if err != nil {
		t.Fatal(err)
	}
	wlog, past, err := war.OpenLog(filepath.Join(t.TempDir(), "war.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := war.NewEngine(theater, wlog, past)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	g, err := gc.New(gc.Deps{Config: testConfig(), Log: log, War: engine})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)
	srv := httptest.NewServer(httpapi.New(g, steamauth.Dev{}, log).Handler())
	defer srv.Close()

	var v gc.WarView
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, err := http.Get(srv.URL + "/v1/war")
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == http.StatusOK {
			v = gc.WarView{}
			if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			// The war opens its first operation on the GC's first tick.
			if len(v.Ops) > 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		res.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("/v1/war: %s", res.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if v.Name != theater.Name || len(v.Fronts) != len(theater.Nodes) || len(v.Links) != len(theater.Edges) {
		t.Fatalf("war view %q: %d fronts, %d links; theater has %d nodes, %d edges", v.Name, len(v.Fronts), len(v.Links), len(theater.Nodes), len(theater.Edges))
	}
	if v.Day != 1 {
		t.Errorf("day %d, want 1 on a new campaign", v.Day)
	}
	for _, f := range v.Fronts {
		if f.Owner != "red" && f.Owner != "blu" {
			t.Errorf("front %s: owner %q", f.ID, f.Owner)
		}
		if f.X <= 0 || f.X >= 1140 || f.Y <= 0 || f.Y >= 640 {
			t.Errorf("front %s at %.0f,%.0f is off the map", f.ID, f.X, f.Y)
		}
		if f.Control < 0 || f.Control > 1 {
			t.Errorf("front %s: control %.2f", f.ID, f.Control)
		}
	}
	if v.Red <= 0 || v.Red >= 1 {
		t.Errorf("red share %.2f: the example theater starts with both sides holding ground", v.Red)
	}

	// An operation: who attacks what, from where, and how far it has got.
	if len(v.Ops) == 0 {
		t.Fatal("no operation open on a fresh campaign")
	}
	op := v.Ops[0]
	if v.Top != op.Node {
		t.Errorf("top %q, want the first operation's sector %q", v.Top, op.Node)
	}
	if op.Attacker == op.Defender || op.From == "" || op.FromName == "" {
		t.Errorf("operation %+v: needs two sides and a sector to push from", op)
	}
	if op.Stage != 1 || op.Stages != len(op.Plan) || op.Stages == 0 {
		t.Errorf("operation %+v: a new one is on stage 1 of its plan", op)
	}
	if op.Momentum != war.MaxMomentum || op.MomentumMax != war.MaxMomentum {
		t.Errorf("operation %+v: a new one has all its momentum", op)
	}
	if !op.Order || v.OrderXP <= 0 {
		t.Errorf("the only operation is not the war's order: %+v", op)
	}
	if op.Conditions == nil || v.Live == nil {
		t.Error("conditions and live battles must be lists, even empty")
	}
	var target *gc.WarNode
	for i := range v.Fronts {
		if v.Fronts[i].ID == op.Node {
			target = &v.Fronts[i]
		}
	}
	if target == nil || target.Front != op.Front || target.Attacker != op.Attacker || target.Mode == "" {
		t.Errorf("the operation's sector does not say it is under attack: %+v", target)
	}
	front := 0
	for _, l := range v.Links {
		if l.Side == "front" {
			front++
		}
	}
	if front == 0 {
		t.Error("no road crosses the front line")
	}
	opened := false
	for _, e := range v.Events {
		opened = opened || (e.Kind == "front_opened" && e.Node == op.Node && e.Name != "")
	}
	if !opened {
		t.Errorf("the report does not say the operation opened: %+v", v.Events)
	}

	// Picking the operation on the war screen counts the player towards it.
	a := newPeer(t, srv.URL, alice)
	a.client()
	a.send(51003, &gcpb.CMsgPartyChatMsg{Msg: proto.String(`{"front":"` + op.Front + `"}`)})
	a.send(51003, &gcpb.CMsgPartyChatMsg{Msg: proto.String(`{"front":"front-999"}`)})
	a.send(51003, &gcpb.CMsgPartyChatMsg{Msg: proto.String(`{"front":"` + op.Front + `"}`)})
	deadline = time.Now().Add(3 * time.Second)
	for {
		res, err := http.Get(srv.URL + "/v1/war")
		if err != nil {
			t.Fatal(err)
		}
		v = gc.WarView{}
		json.NewDecoder(res.Body).Decode(&v)
		res.Body.Close()
		if len(v.Ops) > 0 && v.Ops[0].Front == op.Front && v.Ops[0].Want == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pick did not count: %+v", v.Ops)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

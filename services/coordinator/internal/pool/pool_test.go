package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
)

func staticPool(t *testing.T, n int) *Pool {
	t.Helper()
	var servers []config.StaticServer
	for i := 0; i < n; i++ {
		servers = append(servers, config.StaticServer{
			Name: "s", Connect: string(rune('a'+i)) + ":27015", RCON: "r",
		})
	}
	p, err := New(config.PoolConfig{Providers: []config.ProviderConfig{{Kind: "static", Servers: servers}}}, nil)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return p
}

func TestStaticServerIsHandedOutOnceThenReturned(t *testing.T) {
	p := staticPool(t, 1)
	ctx := context.Background()

	s, err := p.Acquire(ctx, Request{MatchID: "m1"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if p.Free() != 0 || p.InUse() != 1 {
		t.Fatalf("free=%d inUse=%d after acquiring the only server", p.Free(), p.InUse())
	}
	if _, err := p.Acquire(ctx, Request{MatchID: "m2"}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("second acquire err = %v, want ErrNoServer", err)
	}
	if err := p.Release(ctx, s); err != nil {
		t.Fatalf("release: %v", err)
	}
	if p.Free() != 1 || p.InUse() != 0 {
		t.Fatalf("free=%d inUse=%d after release", p.Free(), p.InUse())
	}
}

// A provider that errors must not hide the ones behind it.
type brokenProvider struct{}

func (brokenProvider) Kind() string { return "broken" }
func (brokenProvider) Acquire(context.Context, Request) (*Server, error) {
	return nil, errors.New("the reservation API is down")
}
func (brokenProvider) Release(context.Context, *Server) error { return nil }

func TestABrokenProviderDoesNotBlockTheNextOne(t *testing.T) {
	p := &Pool{}
	p.AddProvider(brokenProvider{})
	p.AddProvider(NewStatic(config.ProviderConfig{Kind: "static", Servers: []config.StaticServer{
		{Name: "good", Connect: "10.0.0.1:27015", RCON: "r"},
	}}))

	s, err := p.Acquire(context.Background(), Request{MatchID: "m"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if s.Connect != "10.0.0.1:27015" {
		t.Fatalf("got %s, want the working provider's server", s.Connect)
	}
}

func TestEveryProviderFailingReportsWhy(t *testing.T) {
	p := &Pool{}
	p.AddProvider(brokenProvider{})

	_, err := p.Acquire(context.Background(), Request{MatchID: "m"})
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer", err)
	}
	if got := err.Error(); got == ErrNoServer.Error() {
		t.Error("the provider's own error was swallowed")
	}
}

func TestRegisteredServerExpiresWithoutHeartbeats(t *testing.T) {
	now := time.Now()
	reg := NewRegistry(30 * time.Second)
	reg.now = func() time.Time { return now }

	reg.Register(Server{Name: "a", Connect: "10.0.0.1:27015"})
	if reg.FreeCount() != 1 {
		t.Fatalf("free = %d after registering", reg.FreeCount())
	}

	now = now.Add(31 * time.Second)
	if reg.FreeCount() != 0 {
		t.Fatal("a server that stopped heartbeating is still being handed out")
	}
	if _, err := reg.Acquire(context.Background(), Request{}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("acquire err = %v, want ErrNoServer", err)
	}

	if !reg.Heartbeat("10.0.0.1:27015") {
		t.Fatal("heartbeat from a known server was refused")
	}
	if reg.FreeCount() != 1 {
		t.Fatal("a heartbeat did not bring the server back")
	}
}

func TestRegisteredServerInAMatchSurvivesPrune(t *testing.T) {
	now := time.Now()
	reg := NewRegistry(30 * time.Second)
	reg.now = func() time.Time { return now }
	reg.Register(Server{Name: "a", Connect: "10.0.0.1:27015"})

	s, err := reg.Acquire(context.Background(), Request{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	now = now.Add(5 * time.Minute)
	reg.Prune()

	if err := reg.Release(context.Background(), s); err != nil {
		t.Fatalf("release: %v", err)
	}
	now = time.Now()
	reg.Register(Server{Name: "a", Connect: "10.0.0.1:27015"})
	if reg.FreeCount() != 1 {
		t.Fatal("the server was pruned while it was running a match")
	}
}

func TestRegionIsRespectedWhenBothSidesNameOne(t *testing.T) {
	st := NewStatic(config.ProviderConfig{Kind: "static", Servers: []config.StaticServer{
		{Name: "eu", Connect: "10.0.0.1:27015", Region: "eu"},
	}})
	if _, err := st.Acquire(context.Background(), Request{Region: "na"}); !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want ErrNoServer for a region we do not serve", err)
	}
	if _, err := st.Acquire(context.Background(), Request{Region: "eu"}); err != nil {
		t.Fatalf("acquire in region: %v", err)
	}
}

// explainedEmpty has nothing free and knows why.
type explainedEmpty struct{}

func (explainedEmpty) Kind() string { return "explained" }
func (explainedEmpty) Acquire(context.Context, Request) (*Server, error) {
	return nil, NoServerReason{Provider: "explained", Reason: "quota spent."}
}
func (explainedEmpty) Release(context.Context, *Server) error { return nil }

func TestAcquireKeepsTheReasonAProviderGave(t *testing.T) {
	p := &Pool{}
	p.AddProvider(NewStatic(config.ProviderConfig{Kind: "static"})) // silently empty
	p.AddProvider(explainedEmpty{})

	_, err := p.Acquire(context.Background(), Request{MatchID: "m"})
	if !errors.Is(err, ErrNoServer) {
		t.Fatalf("err = %v, want it to still read as ErrNoServer", err)
	}
	var reason NoServerReason
	if !errors.As(err, &reason) || reason.Reason != "quota spent." {
		t.Fatalf("err = %v, want the provider's reason carried out of the pool", err)
	}
}

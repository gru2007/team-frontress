package pool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/config"
)

// Serveme reserves a server from a serveme.tf-compatible API.
//
// The flow is the one serveme documents: ask for a prefilled reservation, post
// it to find_servers to learn which are free, then create it. Releasing a
// server deletes the reservation, so these servers are Ephemeral: there is
// nothing to return to a free list.
//
// Two things about the Frontress fork shape this. Its servers are containers
// started for the reservation, so a fresh reservation is not a running server
// for the first half-minute or so: Acquire waits for it rather than handing
// the matchmaker an address that refuses RCON. And it takes the match's own
// password, first map and ruleset with the reservation, so the container boots
// already configured for the match instead of being reconfigured after the
// fact.
type Serveme struct {
	BaseURL string
	APIKey  string
	// ReserveFor is how long a reservation is made for.
	ReserveFor time.Duration
	// ServerConfigID selects a serveme-side server config, if the fork has
	// them. Zero leaves it unset.
	ServerConfigID int
	Region         string
	Client         *http.Client
	// PreferDocker picks a container host over a bare-metal server when
	// serveme offers both. serveme numbers its docker hosts from
	// DockerHost::VIRTUAL_ID_OFFSET (1e9) precisely so they can be told apart
	// in an id, which is what this leans on.
	PreferDocker bool
	// ReadyTimeout bounds the wait for a reservation to come up. Zero uses
	// the caller's deadline, which the matchmaker sets from
	// pool.boot_deadline_secs.
	ReadyTimeout time.Duration
	// PollEvery is how often the reservation is asked whether it is ready.
	PollEvery time.Duration

	mu     sync.Mutex
	active int
}

// dockerHostIDOffset mirrors serveme's DockerHost::VIRTUAL_ID_OFFSET: the
// virtual server ids it gives container hosts start here.
const dockerHostIDOffset = 1_000_000_000

// NewServeme builds a serveme provider from config.
func NewServeme(pc config.ProviderConfig) *Serveme {
	mins := pc.ReserveMins
	if mins <= 0 {
		mins = 120
	}
	return &Serveme{
		BaseURL:        strings.TrimRight(pc.BaseURL, "/"),
		APIKey:         pc.APIKey,
		ReserveFor:     time.Duration(mins) * time.Minute,
		ServerConfigID: pc.ServerConfigID,
		Region:         pc.Region,
		Client:         &http.Client{Timeout: 30 * time.Second},
		PreferDocker:   pc.PreferDocker,
		ReadyTimeout:   time.Duration(pc.ReadyTimeoutSecs) * time.Second,
		PollEvery:      3 * time.Second,
	}
}

func (s *Serveme) Kind() string { return "serveme" }

type servemeReservation struct {
	ID             int64  `json:"id,omitempty"`
	StartsAt       string `json:"starts_at,omitempty"`
	EndsAt         string `json:"ends_at,omitempty"`
	ServerID       int64  `json:"server_id,omitempty"`
	Password       string `json:"password,omitempty"`
	RCON           string `json:"rcon,omitempty"`
	FirstMap       string `json:"first_map,omitempty"`
	ServerConfigID int    `json:"server_config_id,omitempty"`
	EnablePlugins  bool   `json:"enable_plugins,omitempty"`
	AutoEnd        bool   `json:"auto_end,omitempty"`
	StartInstantly bool   `json:"start_instantly,omitempty"`
	Status         string `json:"status,omitempty"`
	// The Frontress fork's matchmaking fields. A serveme that does not know
	// them ignores them; ours writes the match tag and the ruleset into the
	// server's config before it starts.
	MatchID     string `json:"match_id,omitempty"`
	MatchMode   string `json:"match_mode,omitempty"`
	MatchConfig string `json:"match_config,omitempty"`
	Provisioned bool   `json:"provisioned,omitempty"`
	Ended       bool   `json:"ended,omitempty"`
	SDRIP       string `json:"sdr_ip,omitempty"`
	SDRPort     int    `json:"sdr_port,omitempty"`
	SDRTVPort   int    `json:"sdr_tv_port,omitempty"`
	TVPort      int    `json:"tv_port,omitempty"`
	TVPassword  string `json:"tv_password,omitempty"`
	Server      *struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		IP        string `json:"ip"`
		Port      string `json:"port"`
		IPAndPort string `json:"ip_and_port"`
		Flag      string `json:"flag"`
		SDR       bool   `json:"sdr"`
	} `json:"server,omitempty"`
}

type servemeEnvelope struct {
	Reservation servemeReservation   `json:"reservation"`
	Servers     []servemeFoundServer `json:"servers"`
}

// servemeFoundServer is one entry of find_servers. An id at or above
// dockerHostIDOffset is a container host rather than a machine.
type servemeFoundServer struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Location *struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Flag string `json:"flag"`
	} `json:"location"`
}

// Acquire creates a reservation, waits for the server behind it to come up,
// and returns it.
func (s *Serveme) Acquire(ctx context.Context, req Request) (*Server, error) {
	// Step 1: prefilled reservation, mostly for the times serveme wants.
	var prefill servemeEnvelope
	if err := s.do(ctx, http.MethodGet, "/api/reservations/new", nil, &prefill); err != nil {
		return nil, err
	}

	minutes := req.Minutes
	dur := s.ReserveFor
	if minutes > 0 {
		dur = time.Duration(minutes) * time.Minute
	}
	now := time.Now()
	res := servemeReservation{
		StartsAt:       firstNonEmpty(prefill.Reservation.StartsAt, now.Format(time.RFC3339)),
		EndsAt:         now.Add(dur).Format(time.RFC3339),
		AutoEnd:        true,
		StartInstantly: true,
		EnablePlugins:  true,
		FirstMap:       req.Map,
		MatchID:        req.MatchID,
		MatchMode:      req.Mode,
		MatchConfig:    req.ServerConfig,
	}

	// Step 2: which servers are free for that window.
	var found servemeEnvelope
	body := servemeEnvelope{Reservation: res}
	if err := s.do(ctx, http.MethodPost, "/api/reservations/find_servers", body, &found); err != nil {
		return nil, err
	}
	if len(found.Servers) == 0 {
		// serveme answered, and its answer was "nothing". That is a different
		// thing from a provider that could not be reached, and the operator
		// cannot tell them apart from "no server available" alone: an empty
		// list here also covers a spent free-server quota and servers held
		// back as out of date.
		return nil, NoServerReason{
			Provider: "serveme",
			Reason: fmt.Sprintf(
				"%s has no server free for the next %d minutes.",
				s.BaseURL, int(dur.Minutes()),
			),
		}
	}

	// Step 3: create it.
	res.ServerID = s.pick(found.Servers)
	// The match already has a password, and two passwords for one server is
	// one too many: the players would be told ours and the server would be
	// holding serveme's.
	res.Password = firstNonEmpty(req.Password, randomToken(8))
	res.RCON = randomToken(16)
	res.ServerConfigID = s.ServerConfigID
	if found.Reservation.StartsAt != "" {
		res.StartsAt = found.Reservation.StartsAt
	}

	var created servemeEnvelope
	if err := s.do(ctx, http.MethodPost, "/api/reservations", servemeEnvelope{Reservation: res}, &created); err != nil {
		return nil, err
	}
	r := created.Reservation
	if r.ID == 0 {
		return nil, fmt.Errorf("serveme: reservation was not created")
	}

	// A reservation on a container host is a server that does not exist yet.
	// Wait for it: an address that is not listening is worse than a slower
	// match, because the matchmaker would blame the server and re-queue
	// everybody.
	ready, err := s.waitReady(ctx, r.ID)
	if err != nil {
		s.release(context.WithoutCancel(ctx), r.ID)
		return nil, err
	}
	r = ready
	connect := ""
	name := ""
	if r.Server != nil {
		connect = firstNonEmpty(r.Server.IPAndPort, joinHostPort(r.Server.IP, r.Server.Port))
		name = r.Server.Name
	}
	if r.SDRIP != "" && r.SDRPort != 0 {
		// A fork that fronts servers with SDR gives the client a different
		// address than the one we RCON. Only the client-facing one goes in
		// Connect; RCON still uses the real address, which serveme returns in
		// server.ip_and_port.
		connect = fmt.Sprintf("%s:%d", r.SDRIP, r.SDRPort)
	}
	if connect == "" {
		s.release(ctx, r.ID)
		return nil, fmt.Errorf("serveme: reservation %d has no address", r.ID)
	}

	rconAddr := connect
	if r.Server != nil && r.Server.IPAndPort != "" {
		rconAddr = r.Server.IPAndPort
	}

	// SourceTV. serveme reserves the relay along with the server, so the port
	// comes back with the reservation; the host is whichever host players use.
	stv := ""
	if r.TVPort != 0 && r.Server != nil && r.Server.IP != "" {
		stv = fmt.Sprintf("%s:%d", r.Server.IP, r.TVPort)
	}
	if r.SDRIP != "" && r.SDRTVPort != 0 {
		stv = fmt.Sprintf("%s:%d", r.SDRIP, r.SDRTVPort)
	}

	s.mu.Lock()
	s.active++
	s.mu.Unlock()

	return &Server{
		Name:      firstNonEmpty(name, fmt.Sprintf("serveme #%d", r.ID)),
		Connect:   connect,
		RCON:      firstNonEmpty(r.RCON, res.RCON),
		Region:    s.Region,
		STV:       stv,
		Handle:    fmt.Sprintf("%d|%s", r.ID, rconAddr),
		Ephemeral: true,
	}, nil
}

// pick chooses which of the free servers to reserve.
//
// Nothing here knows what a server is: serveme's find_servers answers with a
// list of ids, and the only structure in them is that container hosts are
// numbered from 1e9. Preferring one is preferring "start a container for this
// match" over "take the box somebody set up by hand", which is the whole point
// of the docker-first deployment.
func (s *Serveme) pick(servers []servemeFoundServer) int64 {
	if len(servers) == 0 {
		return 0
	}
	if s.PreferDocker {
		for _, sv := range servers {
			if sv.ID >= dockerHostIDOffset {
				return sv.ID
			}
		}
	}
	return servers[0].ID
}

// waitReady polls a reservation until the server behind it is up.
//
// serveme's own status strings are the contract here: "Ready" and "SDR Ready"
// mean a server that answers, and the failure strings mean it never will. An
// unrecognised status is treated as "still coming up", because a fork that
// invents a new intermediate state should cost us a slower match, not a failed
// one.
func (s *Serveme) waitReady(ctx context.Context, id int64) (servemeReservation, error) {
	if s.ReadyTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.ReadyTimeout)
		defer cancel()
	}
	every := s.PollEvery
	if every <= 0 {
		every = 3 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()

	var last servemeReservation
	for {
		var got servemeEnvelope
		if err := s.do(ctx, http.MethodGet, fmt.Sprintf("/api/reservations/%d", id), nil, &got); err != nil {
			return last, err
		}
		last = got.Reservation
		switch last.Status {
		case "Ready", "SDR Ready":
			return last, nil
		case "Ended", "Ending", "Cloud server failed to start":
			return last, fmt.Errorf("serveme: reservation %d ended before the match started (%s)", id, last.Status)
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("serveme: reservation %d was still %q when we ran out of time: %w",
				id, firstNonEmpty(last.Status, "unknown"), ctx.Err())
		case <-t.C:
		}
	}
}

// Release ends the reservation.
func (s *Serveme) Release(ctx context.Context, sv *Server) error {
	id, _, ok := splitHandle(sv.Handle)
	if !ok {
		return fmt.Errorf("serveme: handle %q is not a reservation", sv.Handle)
	}
	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	s.mu.Unlock()
	return s.release(ctx, id)
}

// RCONAddr is the address to RCON for a server this provider produced, which
// is not always the address the client connects to (SDR).
func RCONAddr(sv *Server) string {
	if sv.Provider != "serveme" {
		return sv.Connect
	}
	if _, addr, ok := splitHandle(sv.Handle); ok && addr != "" {
		return addr
	}
	return sv.Connect
}

func (s *Serveme) release(ctx context.Context, id int64) error {
	return s.do(ctx, http.MethodDelete, fmt.Sprintf("/api/reservations/%d", id), nil, nil)
}

func (s *Serveme) do(ctx context.Context, method, path string, in, out any) error {
	url := s.BaseURL + path
	if strings.Contains(url, "?") {
		url += "&api_key=" + s.APIKey
	} else {
		url += "?api_key=" + s.APIKey
	}

	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("serveme %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound && method == http.MethodDelete {
		return nil // already gone
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("serveme %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(raw), 300))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("serveme %s %s: %w", method, path, err)
	}
	return nil
}

// FreeCount is zero: serveme cannot tell us what is free without a call, and
// this is polled for a status page.
func (s *Serveme) FreeCount() int { return 0 }

func splitHandle(h string) (id int64, addr string, ok bool) {
	i := strings.IndexByte(h, '|')
	if i < 0 {
		return 0, "", false
	}
	if _, err := fmt.Sscanf(h[:i], "%d", &id); err != nil {
		return 0, "", false
	}
	return id, h[i+1:], true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinHostPort(ip, port string) string {
	if ip == "" || port == "" {
		return ""
	}
	return ip + ":" + port
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; a predictable password is
		// worse than a refused reservation, so make the caller notice.
		panic("pool: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)[:n]
}

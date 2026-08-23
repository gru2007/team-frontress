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

	mu     sync.Mutex
	active int
}

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
	SDRIP          string `json:"sdr_ip,omitempty"`
	SDRPort        int    `json:"sdr_port,omitempty"`
	SDRTVPort      int    `json:"sdr_tv_port,omitempty"`
	TVPort         int    `json:"tv_port,omitempty"`
	TVPassword     string `json:"tv_password,omitempty"`
	Server         *struct {
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
	Reservation servemeReservation `json:"reservation"`
	Servers     []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Location *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Flag string `json:"flag"`
		} `json:"location"`
	} `json:"servers"`
}

// Acquire creates a reservation and returns the server it landed on.
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
	}

	// Step 2: which servers are free for that window.
	var found servemeEnvelope
	body := servemeEnvelope{Reservation: res}
	if err := s.do(ctx, http.MethodPost, "/api/reservations/find_servers", body, &found); err != nil {
		return nil, err
	}
	if len(found.Servers) == 0 {
		return nil, ErrNoServer
	}

	// Step 3: create it.
	res.ServerID = found.Servers[0].ID
	res.Password = randomToken(8)
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

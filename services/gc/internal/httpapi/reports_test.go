package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestModerationConsoleAndQueueNeedTheAdminKey(t *testing.T) {
	_, base, stop := startGC(t, nil)
	resp, err := http.Get(base + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	stop()
	if resp.StatusCode != 404 {
		t.Fatal("console served without an admin key", resp.StatusCode)
	}

	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("k", 32)
	_, base, stop = startGCWith(t, cfg, nil)
	defer stop()

	for _, page := range []string{"/admin/", "/admin/console.js", "/admin/console.css"} {
		resp, err := http.Get(base + page)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) == 0 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatal(page, resp.StatusCode, resp.Header)
		}
	}
	if resp, _ := http.Get(base + "/admin/secret.txt"); resp.StatusCode != 404 {
		t.Fatal("console served an unknown file")
	}

	do := func(method, path, body string, auth bool) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+cfg.AdminKey)
		}
		req.Header.Set("X-Frontress-Actor", "76561197960265729")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := do("GET", "/admin/v1/reports", "", false); code != 401 {
		t.Fatal("queue without a key", code)
	}
	code, out := do("GET", "/admin/v1/reports?status=open", "", true)
	if code != 200 || out["cases"] == nil {
		t.Fatal("queue", code, out)
	}
	if code, _ := do("GET", "/admin/v1/reports?status=bogus", "", true); code != 400 {
		t.Fatal("bad status accepted", code)
	}
	if code, _ := do("GET", "/admin/v1/reports/76561197960265740", "", true); code != 404 {
		t.Fatal("unreported player", code)
	}
	if code, _ := do("POST", "/admin/v1/reports/76561197960265740", `{"action":"dismiss","reason":"x"}`, true); code != 409 {
		t.Fatal("dismissed an empty case", code)
	}
	if code, _ := do("POST", "/admin/v1/reports/76561197960265740", `{"action":"nuke","reason":"x"}`, true); code != 400 {
		t.Fatal("unknown action", code)
	}
	if code, _ := do("POST", "/admin/v1/reports/76561197960265740", `{"action":"ban","hours":24,"reason":"aimbot in demo"}`, true); code != 200 {
		t.Fatal("ban", code)
	}
}

package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestPaymentAdministrationIsDisabledOrAuthenticated(t *testing.T) {
	_, base, stop := startGC(t, nil)
	resp, err := http.Get(base + "/admin/v1/payments")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	stop()
	if resp.StatusCode != 404 {
		t.Fatal("admin enabled without a key", resp.StatusCode)
	}
	cfg := testConfig()
	cfg.AdminKey = strings.Repeat("a", 32)
	_, base, stop = startGCWith(t, cfg, nil)
	defer stop()
	for _, auth := range []string{"", "Bearer invalid", cfg.AdminKey, "Bearer " + cfg.AdminKey} {
		req, _ := http.NewRequest(http.MethodGet, base+"/admin/v1/payments", nil)
		req.Header.Set("Authorization", auth)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 401
		if auth == "Bearer "+cfg.AdminKey {
			want = 503
		}
		if resp.StatusCode != want {
			t.Fatal(auth, resp.StatusCode, want)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("admin response can be cached")
		}
	}
}

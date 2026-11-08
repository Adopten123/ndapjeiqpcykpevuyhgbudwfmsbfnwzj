package approvals

import "testing"

func TestApprovalOriginUsesExactIsolatedFrontend(t *testing.T) {
	t.Setenv("RZD_UI_ORIGIN", "http://127.0.0.1:5183")
	for _, origin := range []string{"", "http://127.0.0.1:5173", "http://127.0.0.1:5183"} {
		if !originAllowed(origin) {
			t.Errorf("expected origin %q", origin)
		}
	}
	for _, origin := range []string{"*", "http://localhost:5183", "http://127.0.0.1:5184", "http://127.0.0.1:5183/", "https://example.com"} {
		if originAllowed(origin) {
			t.Errorf("unexpected origin %q", origin)
		}
	}
}

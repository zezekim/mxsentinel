package api

import "testing"

// Only the customer-facing viewer account is reported; the operator's own admin/owner
// sign-ins would otherwise bury the one event that matters.
func TestLoginAlertable(t *testing.T) {
	for role, want := range map[string]bool{
		"viewer":   true,
		"owner":    false,
		"admin":    false,
		"operator": false,
		"":         false,
	} {
		if got := loginAlertable(role); got != want {
			t.Errorf("loginAlertable(%q) = %v, want %v", role, got, want)
		}
	}
}

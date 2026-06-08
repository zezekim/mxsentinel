package rbl

import "testing"

func TestReverseIPv4(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "1.2.3.4", want: "4.3.2.1"},
		{in: "203.0.113.11", want: "11.113.0.203"},
		{in: " 8.8.8.8 ", want: "8.8.8.8"},
		{in: "not-an-ip", wantErr: true},
		{in: "2001:db8::1", wantErr: true}, // IPv6 out of scope
	}
	for _, c := range cases {
		got, err := reverseIPv4(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("reverseIPv4(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("reverseIPv4(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("reverseIPv4(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQueryName(t *testing.T) {
	got, err := queryName("1.2.3.4", "zen.spamhaus.org.")
	if err != nil {
		t.Fatalf("queryName: %v", err)
	}
	if want := "4.3.2.1.zen.spamhaus.org"; got != want {
		t.Errorf("queryName = %q, want %q", got, want)
	}
}

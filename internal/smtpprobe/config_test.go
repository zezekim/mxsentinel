package smtpprobe

import (
	"reflect"
	"testing"
	"time"
)

func TestModeForPort(t *testing.T) {
	cases := map[int]Mode{25: ModePlain, 587: ModeSTARTTLS, 465: ModeImplicitTLS, 2525: ModePlain}
	for port, want := range cases {
		if got := ModeForPort(port); got != want {
			t.Errorf("ModeForPort(%d) = %q, want %q", port, got, want)
		}
	}
}

func TestParseEndpoints(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want []Endpoint
	}{
		{
			name: "explicit modes and port-derived modes",
			spec: "relay.example.com:587, relay.example.com:465, relay.example.com:25:starttls",
			want: []Endpoint{
				{Host: "relay.example.com", Port: 587, Mode: ModeSTARTTLS},
				{Host: "relay.example.com", Port: 465, Mode: ModeImplicitTLS},
				{Host: "relay.example.com", Port: 25, Mode: ModeSTARTTLS},
			},
		},
		{
			name: "dedupe and skip garbage",
			spec: "a:25, a:25, b:notaport, :587, c:587:bogusmode",
			want: []Endpoint{
				{Host: "a", Port: 25, Mode: ModePlain},
				{Host: "c", Port: 587, Mode: ModeSTARTTLS},
			},
		},
		{
			name: "empty",
			spec: "   ",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEndpoints(tt.spec)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseEndpoints(%q)\n got  %+v\n want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("MXS_PROBE_ENDPOINTS", "relay.test:587:starttls")
	t.Setenv("MXS_PROBE_INTERVAL", "30s")
	t.Setenv("MXS_PROBE_CERT_WARN", "168h")
	t.Setenv("MXS_PROBE_CHECK_RESPONSE", "true")
	t.Setenv("MXS_PROBE_TENANT_ID", "11111111-1111-1111-1111-111111111111")

	c := LoadConfig()
	if len(c.Endpoints) != 1 || c.Endpoints[0].Port != 587 || c.Endpoints[0].Mode != ModeSTARTTLS {
		t.Fatalf("endpoints = %+v", c.Endpoints)
	}
	if c.Interval != 30*time.Second {
		t.Errorf("interval = %v", c.Interval)
	}
	if c.CertWarnThreshold != 168*time.Hour {
		t.Errorf("certWarn = %v", c.CertWarnThreshold)
	}
	if !c.CheckResponse {
		t.Errorf("checkResponse should be true")
	}
	if c.TenantID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("tenantID = %q", c.TenantID)
	}
}

func TestLoadConfigHostPortsExpansion(t *testing.T) {
	t.Setenv("MXS_PROBE_ENDPOINTS", "")
	t.Setenv("MXS_PROBE_HOST", "relay.test")
	t.Setenv("MXS_PROBE_PORTS", "25,587")
	c := LoadConfig()
	want := []Endpoint{
		{Host: "relay.test", Port: 25, Mode: ModePlain},
		{Host: "relay.test", Port: 587, Mode: ModeSTARTTLS},
	}
	if !reflect.DeepEqual(c.Endpoints, want) {
		t.Fatalf("endpoints = %+v, want %+v", c.Endpoints, want)
	}
}

func TestLoadConfigInvalidIntervalFallsBack(t *testing.T) {
	t.Setenv("MXS_PROBE_INTERVAL", "not-a-duration")
	if c := LoadConfig(); c.Interval != DefaultInterval {
		t.Fatalf("interval = %v, want default %v", c.Interval, DefaultInterval)
	}
}

package mtasts

import (
	"reflect"
	"testing"
)

func TestParsePolicy(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    Policy
		wantErr bool
	}{
		{
			name: "enforce with two mx",
			body: "version: STSv1\nmode: enforce\nmx: mail.example.com\nmx: *.example.net\nmax_age: 604800\n",
			want: Policy{Version: "STSv1", Mode: ModeEnforce, MX: []string{"mail.example.com", "*.example.net"}, MaxAge: 604800},
		},
		{
			name: "crlf line endings and testing mode",
			body: "version: STSv1\r\nmode: testing\r\nmx: mx1.example.com\r\nmax_age: 86400\r\n",
			want: Policy{Version: "STSv1", Mode: ModeTesting, MX: []string{"mx1.example.com"}, MaxAge: 86400},
		},
		{
			name: "mode none needs no mx",
			body: "version: STSv1\nmode: none\nmax_age: 0\n",
			want: Policy{Version: "STSv1", Mode: ModeNone, MaxAge: 0},
		},
		{
			name: "unknown keys ignored",
			body: "version: STSv1\nmode: enforce\nmx: a.example.com\nmax_age: 100\nfuture_key: whatever\n",
			want: Policy{Version: "STSv1", Mode: ModeEnforce, MX: []string{"a.example.com"}, MaxAge: 100},
		},
		{name: "missing version", body: "mode: enforce\nmx: a.example.com\nmax_age: 1\n", wantErr: true},
		{name: "wrong version", body: "version: STSv2\nmode: enforce\nmx: a\nmax_age: 1\n", wantErr: true},
		{name: "invalid mode", body: "version: STSv1\nmode: bogus\nmx: a\nmax_age: 1\n", wantErr: true},
		{name: "enforce without mx", body: "version: STSv1\nmode: enforce\nmax_age: 1\n", wantErr: true},
		{name: "bad max_age", body: "version: STSv1\nmode: enforce\nmx: a\nmax_age: soon\n", wantErr: true},
		{name: "malformed line", body: "version STSv1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePolicy(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePolicy() err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParsePolicy() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseTXT(t *testing.T) {
	tests := []struct {
		name    string
		txt     string
		wantID  string
		wantErr bool
	}{
		{name: "valid", txt: "v=STSv1; id=20240101T000000Z", wantID: "20240101T000000Z"},
		{name: "extra spacing", txt: "v=STSv1;id=abc123 ;", wantID: "abc123"},
		{name: "missing id", txt: "v=STSv1;", wantErr: true},
		{name: "wrong version", txt: "v=STSv2; id=x", wantErr: true},
		{name: "empty", txt: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := ParseTXT(tt.txt)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseTXT() err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if rec.ID != tt.wantID {
				t.Errorf("ParseTXT() id = %q, want %q", rec.ID, tt.wantID)
			}
		})
	}
}

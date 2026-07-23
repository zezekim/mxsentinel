package smtpprobe

import (
	"reflect"
	"testing"
)

func TestParseEHLO(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  Capabilities
	}{
		{
			name: "full submission server",
			lines: []string{
				"mail.example.com ESMTP Postfix",
				"PIPELINING",
				"SIZE 35882577",
				"STARTTLS",
				"AUTH PLAIN LOGIN",
				"ENHANCEDSTATUSCODES",
				"8BITMIME",
			},
			want: Capabilities{
				Greeting:     "mail.example.com ESMTP Postfix",
				STARTTLS:     true,
				Auth:         true,
				AuthMechs:    []string{"PLAIN", "LOGIN"},
				Pipelining:   true,
				EightBitMIME: true,
				Enhanced:     true,
				Size:         35882577,
				Keywords:     []string{"PIPELINING", "SIZE", "STARTTLS", "AUTH", "ENHANCEDSTATUSCODES", "8BITMIME"},
			},
		},
		{
			name:  "case-insensitive keywords, no auth",
			lines: []string{"relay", "starttls", "size 0"},
			want: Capabilities{
				Greeting: "relay",
				STARTTLS: true,
				Size:     0,
				Keywords: []string{"STARTTLS", "SIZE"},
			},
		},
		{
			name:  "auth with equals-style continuation is still a keyword",
			lines: []string{"h", "AUTH=PLAIN", "AUTH LOGIN"},
			want: Capabilities{
				Greeting:  "h",
				Auth:      true,
				AuthMechs: []string{"LOGIN"},
				Keywords:  []string{"AUTH=PLAIN", "AUTH"},
			},
		},
		{
			name:  "empty",
			lines: nil,
			want:  Capabilities{},
		},
		{
			name:  "greeting only",
			lines: []string{"just.a.banner"},
			want:  Capabilities{Greeting: "just.a.banner"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseEHLO(tt.lines)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseEHLO(%v)\n got  %+v\n want %+v", tt.lines, got, tt.want)
			}
		})
	}
}

func TestSplitReplyLines(t *testing.T) {
	got := splitReplyLines("greeting\nPIPELINING\nSTARTTLS")
	want := []string{"greeting", "PIPELINING", "STARTTLS"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitReplyLines got %v want %v", got, want)
	}
	if splitReplyLines("") != nil {
		t.Fatalf("splitReplyLines(\"\") should be nil")
	}
}

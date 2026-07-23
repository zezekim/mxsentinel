package tlsrpt

import "testing"

// sampleReport is the RFC 8460 §A.1 example report (lightly trimmed).
const sampleReport = `{
  "organization-name": "Company-X",
  "date-range": {
    "start-datetime": "2016-04-01T00:00:00Z",
    "end-datetime": "2016-04-01T23:59:59Z"
  },
  "contact-info": "sts-reporting@company-x.example",
  "report-id": "5065427c-23d3-47ca-b6e0-946ea0e8c4be",
  "policies": [{
    "policy": {
      "policy-type": "sts",
      "policy-string": ["version: STSv1","mode: testing","mx: *.mail.company-y.example","max_age: 86400"],
      "policy-domain": "company-y.example",
      "mx-host": ["*.mail.company-y.example"]
    },
    "summary": {
      "total-successful-session-count": 5326,
      "total-failure-session-count": 303
    },
    "failure-details": [{
      "result-type": "certificate-expired",
      "sending-mta-ip": "2001:db8:abcd:0012::1",
      "receiving-mx-hostname": "mx1.mail.company-y.example",
      "failed-session-count": 100
    }, {
      "result-type": "starttls-not-supported",
      "sending-mta-ip": "2001:db8:abcd:0013::1",
      "receiving-mx-hostname": "mx2.mail.company-y.example",
      "receiving-ip": "203.0.113.56",
      "failed-session-count": 200,
      "additional-information": "https://reports.company-x.example/report_info?id=5065427c#StarttlsNotSupported"
    }]
  }]
}`

func TestParseBytes(t *testing.T) {
	rep, err := ParseBytes([]byte(sampleReport))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if rep.OrganizationName != "Company-X" {
		t.Errorf("org = %q", rep.OrganizationName)
	}
	if rep.ReportID != "5065427c-23d3-47ca-b6e0-946ea0e8c4be" {
		t.Errorf("report-id = %q", rep.ReportID)
	}
	if rep.PrimaryDomain() != "company-y.example" {
		t.Errorf("primary domain = %q", rep.PrimaryDomain())
	}
	if got := rep.DateBegin.Format("2006-01-02"); got != "2016-04-01" {
		t.Errorf("date begin = %q", got)
	}
	if len(rep.Policies) != 1 {
		t.Fatalf("policies = %d", len(rep.Policies))
	}
	p := rep.Policies[0]
	if p.PolicyType != "sts" || p.PolicyDomain != "company-y.example" {
		t.Errorf("policy = %+v", p)
	}
	if p.SuccessCount != 5326 || p.FailureCount != 303 {
		t.Errorf("summary success=%d failure=%d", p.SuccessCount, p.FailureCount)
	}
	if len(p.FailureDetails) != 2 {
		t.Fatalf("failure details = %d", len(p.FailureDetails))
	}
	if p.FailureDetails[0].ResultType != "certificate-expired" || p.FailureDetails[0].FailedSessionCount != 100 {
		t.Errorf("failure[0] = %+v", p.FailureDetails[0])
	}

	s, f := rep.Totals()
	if s != 5326 || f != 303 {
		t.Errorf("totals success=%d failure=%d", s, f)
	}
}

func TestParseBytesErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed json", "{not json"},
		{"missing report-id", `{"organization-name":"X","policies":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseBytes([]byte(tt.body)); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

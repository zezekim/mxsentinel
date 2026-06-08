package authwatch

import (
	"testing"
	"time"
)

func testConfig() Config {
	c := DefaultConfig()
	c.Window = time.Hour
	c.Cooldown = time.Hour
	c.DistinctRcptThreshold = 10
	c.MinVolume = 5
	c.BounceRate = 0.5
	c.OffHoursWeight = 0 // isolate from clock in most tests
	return c
}

func TestRecipientBurstTrips(t *testing.T) {
	d := NewDetector(testConfig())
	now := time.Now()
	var tripped bool
	for i := 0; i < 12; i++ {
		_, _, _, ok := d.Observe(Observation{
			TenantID:        "t1",
			SASLUsername:    "mailer@example.com",
			FromDomain:      "example.com",
			RecipientDomain: rcptDomain(i),
			At:              now.Add(time.Duration(i) * time.Second),
		})
		if ok {
			tripped = true
		}
	}
	if !tripped {
		t.Fatal("expected recipient-burst trip across 12 distinct recipient domains")
	}
}

func TestEmptyUsernameIgnored(t *testing.T) {
	d := NewDetector(testConfig())
	if _, _, _, ok := d.Observe(Observation{SASLUsername: "", RecipientDomain: "a.com"}); ok {
		t.Fatal("empty SASL username must never trip")
	}
}

func TestBounceSpikeTrips(t *testing.T) {
	d := NewDetector(testConfig())
	now := time.Now()
	var tripped bool
	// 10 messages, all abuse bounces to a single recipient domain -> bounce-rate spike only.
	for i := 0; i < 10; i++ {
		det, _, _, ok := d.Observe(Observation{
			TenantID:        "t1",
			SASLUsername:    "blast@example.com",
			FromDomain:      "example.com",
			RecipientDomain: "gmail.com",
			Abuse:           true,
			At:              now.Add(time.Duration(i) * time.Second),
		})
		if ok {
			tripped = true
			if det == nil {
				t.Fatal("trip returned nil detail")
			}
			found := false
			for _, c := range det.Contributors {
				if c == SignalBounceSpike {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected bounce_spike contributor, got %v", det.Contributors)
			}
		}
	}
	if !tripped {
		t.Fatal("expected bounce-rate spike trip")
	}
}

func TestCooldownSuppressesSecondTrip(t *testing.T) {
	d := NewDetector(testConfig())
	now := time.Now()
	trips := 0
	for i := 0; i < 40; i++ {
		_, _, _, ok := d.Observe(Observation{
			TenantID:        "t1",
			SASLUsername:    "mailer@example.com",
			RecipientDomain: rcptDomain(i),
			At:              now.Add(time.Duration(i) * time.Second),
		})
		if ok {
			trips++
		}
	}
	if trips != 1 {
		t.Fatalf("cooldown should allow exactly 1 trip in window, got %d", trips)
	}
}

func TestOffHoursBand(t *testing.T) {
	c := Config{OffHoursStart: 22, OffHoursEnd: 5}
	if !c.IsOffHours(23) || !c.IsOffHours(2) {
		t.Fatal("wrapping band should include 23 and 2")
	}
	if c.IsOffHours(12) {
		t.Fatal("noon should not be off-hours")
	}
	c2 := Config{OffHoursStart: 1, OffHoursEnd: 5}
	if !c2.IsOffHours(3) || c2.IsOffHours(6) {
		t.Fatal("non-wrapping band check failed")
	}
}

func rcptDomain(i int) string {
	return string(rune('a'+i)) + ".example.net"
}

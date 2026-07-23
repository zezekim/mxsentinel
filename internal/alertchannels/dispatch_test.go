package alertchannels

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore is an in-memory DeliveryStore for deterministic dedup/throttle tests.
type fakeStore struct {
	// records is the append-only log of (channelID, alertRef, status).
	records []fakeRec
	// delivered controls DeliveredFor's answer per (channel|alertRef).
	delivered map[string]bool
	// recentSent controls LastSentToChannel's answer per channel.
	recentSent map[string]bool
	recordErr  error
}

type fakeRec struct{ channelID, alertRef, status, err string }

func newFakeStore() *fakeStore {
	return &fakeStore{delivered: map[string]bool{}, recentSent: map[string]bool{}}
}

func (f *fakeStore) DeliveredFor(_ context.Context, channelID, alertRef string, _ time.Duration) (bool, error) {
	return f.delivered[channelID+"|"+alertRef], nil
}
func (f *fakeStore) LastSentToChannel(_ context.Context, channelID string, _ time.Duration) (bool, error) {
	return f.recentSent[channelID], nil
}
func (f *fakeStore) Record(_ context.Context, channelID, alertRef, status, errMsg string) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.records = append(f.records, fakeRec{channelID, alertRef, status, errMsg})
	return nil
}

// fakeNotifier records sends and can be told to fail.
type fakeNotifier struct {
	typ  string
	sent int
	fail error
}

func (f *fakeNotifier) Type() string { return f.typ }
func (f *fakeNotifier) Send(_ context.Context, _ Notification, _ map[string]any) error {
	f.sent++
	return f.fail
}

func newDispatcher(store DeliveryStore, n Notifier) (*Dispatcher, *fakeNotifier) {
	fn := n.(*fakeNotifier)
	return &Dispatcher{
		Notifiers: map[string]Notifier{fn.typ: fn},
		Store:     store,
		Throttle:  15 * time.Minute,
		Dedup:     6 * time.Hour,
	}, fn
}

var testChannel = Channel{ID: "ch1", TenantID: "t1", Type: "webhook", Name: "hook", Enabled: true, Config: []byte(`{"url":"https://x"}`)}
var testNote = Notification{AlertRef: "inc-1", Title: "t", Severity: "critical"}

func TestDispatchSends(t *testing.T) {
	store := newFakeStore()
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if len(res) != 1 || res[0].Status != StatusSent {
		t.Fatalf("expected sent, got %+v", res)
	}
	if fn.sent != 1 {
		t.Errorf("notifier.Send called %d times, want 1", fn.sent)
	}
	if len(store.records) != 1 || store.records[0].status != StatusSent {
		t.Errorf("expected one 'sent' record, got %+v", store.records)
	}
}

func TestDispatchDedup(t *testing.T) {
	store := newFakeStore()
	store.delivered["ch1|inc-1"] = true // already delivered this alert to this channel
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if res[0].Status != StatusSkippedDedup {
		t.Fatalf("expected skipped_dedup, got %s", res[0].Status)
	}
	if fn.sent != 0 {
		t.Errorf("notifier should not have been called on dedup")
	}
	if store.records[0].status != StatusSkippedDedup {
		t.Errorf("expected skipped_dedup record, got %s", store.records[0].status)
	}
}

func TestDispatchThrottle(t *testing.T) {
	store := newFakeStore()
	store.recentSent["ch1"] = true // a send happened within the throttle window
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if res[0].Status != StatusSkippedThrottle {
		t.Fatalf("expected skipped_throttle, got %s", res[0].Status)
	}
	if fn.sent != 0 {
		t.Errorf("notifier should not have been called on throttle")
	}
}

func TestDispatchDedupBeatsThrottle(t *testing.T) {
	// When both would trigger, dedup is checked first.
	store := newFakeStore()
	store.delivered["ch1|inc-1"] = true
	store.recentSent["ch1"] = true
	d, _ := newDispatcher(store, &fakeNotifier{typ: "webhook"})
	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if res[0].Status != StatusSkippedDedup {
		t.Fatalf("expected dedup to win, got %s", res[0].Status)
	}
}

func TestDispatchTestBypassesThrottleAndDedup(t *testing.T) {
	store := newFakeStore()
	store.delivered["ch1|inc-1"] = true
	store.recentSent["ch1"] = true
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	note := testNote
	note.Test = true
	res := d.Dispatch(context.Background(), []Channel{testChannel}, note)
	if res[0].Status != StatusSent {
		t.Fatalf("test notification should bypass dedup/throttle, got %s", res[0].Status)
	}
	if fn.sent != 1 {
		t.Errorf("expected notifier to be called for test send")
	}
}

func TestDispatchDisabledChannelSkipped(t *testing.T) {
	store := newFakeStore()
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})
	ch := testChannel
	ch.Enabled = false
	res := d.Dispatch(context.Background(), []Channel{ch}, testNote)
	if res[0].Status != StatusSkippedThrottle {
		t.Fatalf("disabled channel expected skip, got %s", res[0].Status)
	}
	if fn.sent != 0 {
		t.Errorf("disabled channel should not send")
	}
}

func TestDispatchNotifierFailureRecorded(t *testing.T) {
	store := newFakeStore()
	d, _ := newDispatcher(store, &fakeNotifier{typ: "webhook", fail: errors.New("boom")})
	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if res[0].Status != StatusFailed {
		t.Fatalf("expected failed, got %s", res[0].Status)
	}
	if res[0].Err != "boom" {
		t.Errorf("err = %q, want boom", res[0].Err)
	}
	if store.records[0].status != StatusFailed {
		t.Errorf("expected failed record")
	}
}

func TestDispatchNoDriver(t *testing.T) {
	store := newFakeStore()
	d, _ := newDispatcher(store, &fakeNotifier{typ: "webhook"})
	ch := testChannel
	ch.Type = "unknown"
	res := d.Dispatch(context.Background(), []Channel{ch}, testNote)
	if res[0].Status != StatusFailed {
		t.Fatalf("expected failed for missing driver, got %s", res[0].Status)
	}
}

func TestDispatchThrottleDisabledWhenZero(t *testing.T) {
	store := newFakeStore()
	store.recentSent["ch1"] = true
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})
	d.Throttle = 0 // disabled
	d.Dedup = 0    // disabled
	res := d.Dispatch(context.Background(), []Channel{testChannel}, testNote)
	if res[0].Status != StatusSent {
		t.Fatalf("with throttle/dedup disabled, expected sent, got %s", res[0].Status)
	}
	if fn.sent != 1 {
		t.Errorf("expected send when windows are zero")
	}
}

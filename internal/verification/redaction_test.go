package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRedactionStore struct {
	queue       []DueVerification
	listErr     error
	markErr     error
	marked      []uuid.UUID
	listCalls   int
	lastRetain  int
	lastHoldCap int
	lastBatch   int
}

func (fake *fakeRedactionStore) ListDueForRedaction(_ context.Context, retentionDays, holdCapDays, batchSize int) ([]DueVerification, error) {
	fake.listCalls++
	fake.lastRetain, fake.lastHoldCap, fake.lastBatch = retentionDays, holdCapDays, batchSize
	if fake.listErr != nil {
		return nil, fake.listErr
	}
	take := batchSize
	if take > len(fake.queue) {
		take = len(fake.queue)
	}
	batch := append([]DueVerification(nil), fake.queue[:take]...)
	return batch, nil
}

func (fake *fakeRedactionStore) MarkRedacted(_ context.Context, id uuid.UUID) error {
	if fake.markErr != nil {
		return fake.markErr
	}
	fake.marked = append(fake.marked, id)
	remaining := fake.queue[:0]
	for _, item := range fake.queue {
		if item.ID != id {
			remaining = append(remaining, item)
		}
	}
	fake.queue = remaining
	return nil
}

type fakeRedactor struct {
	failFor map[string]error
	calls   []string
}

func (fake *fakeRedactor) RedactInquiry(_ context.Context, inquiryID string) error {
	fake.calls = append(fake.calls, inquiryID)
	return fake.failFor[inquiryID]
}

func dueItems(count int) []DueVerification {
	items := make([]DueVerification, count)
	for index := range items {
		items[index] = DueVerification{ID: uuid.New(), AccountID: uuid.New(), InquiryID: "inq_" + uuid.NewString()[:8]}
	}
	return items
}

func newTestRedactionService(store RedactionStore, redactor InquiryRedactor, batch int) *RedactionService {
	service := NewRedactionService(store, redactor, RedactionConfig{Enabled: true, RetentionDays: 30, HoldCapDays: 1095, Interval: time.Hour, BatchSize: batch})
	service.pause = 0
	return service
}

func TestRedactionRunOnceRedactsAndRecordsEachDueInquiry(t *testing.T) {
	store := &fakeRedactionStore{queue: dueItems(3)}
	redactor := &fakeRedactor{}
	service := newTestRedactionService(store, redactor, 10)
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RedactionReport{Due: 3, Redacted: 3}) {
		t.Fatalf("report = %+v", report)
	}
	if len(redactor.calls) != 3 || len(store.marked) != 3 {
		t.Fatalf("calls = %d marked = %d", len(redactor.calls), len(store.marked))
	}
	if store.lastRetain != 30 || store.lastHoldCap != 1095 || store.lastBatch != 10 {
		t.Fatalf("store asked for %d/%d/%d", store.lastRetain, store.lastHoldCap, store.lastBatch)
	}
}

func TestRedactionFailureOnOneInquiryNeverBlocksOrMarksTheOthers(t *testing.T) {
	items := dueItems(3)
	store := &fakeRedactionStore{queue: append([]DueVerification(nil), items...)}
	redactor := &fakeRedactor{failFor: map[string]error{items[1].InquiryID: ErrPersonaRequest}}
	service := newTestRedactionService(store, redactor, 10)
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report != (RedactionReport{Due: 3, Redacted: 2, Failed: 1}) {
		t.Fatalf("report = %+v", report)
	}
	for _, id := range store.marked {
		if id == items[1].ID {
			t.Fatal("an inquiry whose redaction failed must not be marked redacted")
		}
	}
}

func TestRedactionRecordFailureIsCountedAndRetriedNextCycle(t *testing.T) {
	store := &fakeRedactionStore{queue: dueItems(2), markErr: errors.New("db down")}
	service := newTestRedactionService(store, &fakeRedactor{}, 10)
	report, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Redacted != 0 || report.Failed != 2 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.queue) != 2 {
		t.Fatal("unmarked inquiries must stay due")
	}
}

func TestRedactionRunOnceReturnsListErrors(t *testing.T) {
	store := &fakeRedactionStore{listErr: errors.New("db down")}
	service := newTestRedactionService(store, &fakeRedactor{}, 10)
	if _, err := service.RunOnce(context.Background()); err == nil {
		t.Fatal("expected list error")
	}
}

func TestRedactionRunOnceStopsWhenContextIsCancelled(t *testing.T) {
	store := &fakeRedactionStore{queue: dueItems(5)}
	redactor := &fakeRedactor{}
	service := newTestRedactionService(store, redactor, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(redactor.calls) != 0 {
		t.Fatalf("redacted after cancel: %v", redactor.calls)
	}
}

func TestRedactionCycleDrainsEveryBatchWhenBacklogIsLarge(t *testing.T) {
	store := &fakeRedactionStore{queue: dueItems(25)}
	redactor := &fakeRedactor{}
	service := newTestRedactionService(store, redactor, 10)
	total := service.RunCycle(context.Background())
	if total.Redacted != 25 || len(store.queue) != 0 {
		t.Fatalf("total = %+v remaining = %d", total, len(store.queue))
	}
}

func TestRedactionCycleStopsWhenNothingCanBeRedacted(t *testing.T) {
	items := dueItems(10)
	failures := map[string]error{}
	for _, item := range items {
		failures[item.InquiryID] = ErrPersonaRequest
	}
	store := &fakeRedactionStore{queue: items}
	service := newTestRedactionService(store, &fakeRedactor{failFor: failures}, 10)
	total := service.RunCycle(context.Background())
	if total.Failed != 10 || store.listCalls != 1 {
		t.Fatalf("total = %+v list calls = %d", total, store.listCalls)
	}
}

func TestRedactionRunDoesNothingWhenDisabled(t *testing.T) {
	store := &fakeRedactionStore{queue: dueItems(2)}
	service := NewRedactionService(store, &fakeRedactor{}, RedactionConfig{Enabled: false, BatchSize: 10})
	done := make(chan struct{})
	go func() {
		service.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled service must return immediately")
	}
	if store.listCalls != 0 {
		t.Fatal("a disabled service must not touch the store")
	}
}

func TestRedactionRunStopsWhenContextIsCancelled(t *testing.T) {
	store := &fakeRedactionStore{}
	service := newTestRedactionService(store, &fakeRedactor{}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if store.listCalls < 1 {
		t.Fatal("Run should check for due inquiries at start")
	}
}

func TestRedactionConfigDefaults(t *testing.T) {
	config, err := redactionConfigFromLookup(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	want := RedactionConfig{Enabled: true, RetentionDays: 30, HoldCapDays: 1095, Interval: 6 * time.Hour, BatchSize: 50}
	if config != want {
		t.Fatalf("config = %+v", config)
	}
}

func TestRedactionConfigOverridesAndRejectsBadValues(t *testing.T) {
	lookupFor := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	good, err := redactionConfigFromLookup(lookupFor(map[string]string{"ID_RETENTION_DAYS": "7", "ID_HOLD_CAP_DAYS": "365", "ID_PURGE_BATCH": "5", "ID_PURGE_INTERVAL": "90m", "ID_PURGE_ENABLED": "false"}))
	if err != nil {
		t.Fatal(err)
	}
	if good.Enabled || good.RetentionDays != 7 || good.HoldCapDays != 365 || good.BatchSize != 5 || good.Interval != 90*time.Minute {
		t.Fatalf("config = %+v", good)
	}
	bad := []map[string]string{
		{"ID_RETENTION_DAYS": "0"},
		{"ID_RETENTION_DAYS": "-5"},
		{"ID_RETENTION_DAYS": "1096"},
		{"ID_RETENTION_DAYS": "thirty"},
		{"ID_HOLD_CAP_DAYS": "10", "ID_RETENTION_DAYS": "30"},
		{"ID_PURGE_BATCH": "0"},
		{"ID_PURGE_BATCH": "501"},
		{"ID_PURGE_INTERVAL": "5s"},
		{"ID_PURGE_INTERVAL": "soon"},
		{"ID_PURGE_ENABLED": "maybe"},
	}
	for _, values := range bad {
		if _, err := redactionConfigFromLookup(lookupFor(values)); err == nil {
			t.Fatalf("expected an error for %v", values)
		}
	}
}

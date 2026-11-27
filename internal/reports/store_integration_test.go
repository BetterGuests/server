package reports_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/bookings"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/BetterGuests/BetterGuests-Server/internal/reports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type world struct {
	t            *testing.T
	ctx          context.Context
	pool         *pgxpool.Pool
	superuser    *pgx.Conn
	service      *reports.ReportService
	host         uuid.UUID
	guest        uuid.UUID
	admin        uuid.UUID
	listing      uuid.UUID
	booking      uuid.UUID
	conversation uuid.UUID
}

func (w *world) sql(statement string, args ...any) {
	w.t.Helper()
	if _, err := w.superuser.Exec(w.ctx, statement, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) account() uuid.UUID {
	w.t.Helper()
	return dbtest.SignedUpAccount(w.t, w.ctx, w.pool)
}

func newWorld(t *testing.T) *world {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	superuser, err := pgx.Connect(ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = superuser.Close(context.Background()) })

	w := &world{t: t, ctx: ctx, pool: pool, superuser: superuser}
	w.host, w.guest, w.admin = w.account(), w.account(), w.account()
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, w.admin, w.admin.String()+"@example.test")
	t.Cleanup(func() {
		_, _ = superuser.Exec(context.Background(), `DELETE FROM app.admin_accounts WHERE account_id = $1`, w.admin)
	})
	w.service = reports.NewReportService(
		reports.NewPostgresReportStore(pool),
		proofs.NewProofService(proofs.NewPostgresProofStore(pool), nil),
	)

	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, w.host, listings.CreateListingInput{
		Title: "Report House", Description: "A complete listing used by the report integration test.",
		Address: listings.Address{Line1: "1 Report Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.listing = listing.ID
	w.sql(`UPDATE app.listings SET status = 'published', host_name = 'Hana Host' WHERE id = $1`, listing.ID)
	w.sql(`INSERT INTO app.account_names (account_id, display_name) VALUES ($1, 'Gil Guest')
	       ON CONFLICT (account_id) DO UPDATE SET display_name = excluded.display_name`, w.guest)

	start := time.Now().UTC().AddDate(0, 0, 20)
	booking, err := bookings.NewBookingService(bookings.NewPostgresBookingStore(pool)).CreateBooking(ctx, w.guest, bookings.CreateBookingInput{
		ListingID: w.listing, StartsOn: start, EndsOn: start.AddDate(0, 0, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.booking = booking.ID
	w.conversation, err = messages.NewPostgresMessageStore(pool).Open(ctx, w.guest, uuid.New(), messages.OpenTarget{ListingID: w.listing}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) file(reporter uuid.UUID, submission reports.Submission) error {
	submission.Details = "Something happened."
	_, err := w.service.File(w.ctx, reporter, submission)
	return err
}

func (w *world) openAbout(subject uuid.UUID) []reports.Report {
	w.t.Helper()
	all, err := w.service.List(w.ctx, w.admin, true)
	if err != nil {
		w.t.Fatal(err)
	}
	var about []reports.Report
	for _, report := range all {
		if report.SubjectID == subject {
			about = append(about, report)
		}
	}
	return about
}

func TestAListingAHostAndAGuestCanEachBeReportedAndReviewed(t *testing.T) {
	w := newWorld(t)
	if err := w.file(w.guest, reports.Submission{Target: reports.TargetListing, Reason: reports.ReasonInaccurate, ListingID: w.listing}); err != nil {
		t.Fatalf("report the listing: %v", err)
	}
	if err := w.file(w.guest, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonHarassment, BookingID: w.booking}); err != nil {
		t.Fatalf("report the host: %v", err)
	}
	if err := w.file(w.host, reports.Submission{Target: reports.TargetGuest, Reason: reports.ReasonPropertyDamage, ConversationID: w.conversation}); err != nil {
		t.Fatalf("report the guest: %v", err)
	}

	aboutHost := w.openAbout(w.host)
	if len(aboutHost) != 2 {
		t.Fatalf("reports about the host = %+v", aboutHost)
	}
	for _, report := range aboutHost {
		if report.SubjectName != "Hana Host" || report.ReporterName != "Gil Guest" || report.ListingTitle != "Report House" || report.SubjectReports != 2 {
			t.Fatalf("report = %+v", report)
		}
	}
	aboutGuest := w.openAbout(w.guest)
	if len(aboutGuest) != 1 || aboutGuest[0].SubjectName != "Gil Guest" || !aboutGuest[0].FromConversation {
		t.Fatalf("reports about the guest = %+v", aboutGuest)
	}

	target := aboutGuest[0].ID
	if err := w.service.Resolve(w.ctx, w.admin, target, reports.Decision{Outcome: reports.StatusActioned, Note: "Warned."}); err != nil {
		t.Fatal(err)
	}
	if err := w.service.Resolve(w.ctx, w.admin, target, reports.Decision{Outcome: reports.StatusDismissed}); !errors.Is(err, reports.ErrReportNotFound) {
		t.Fatalf("closed twice: %v", err)
	}
	if left := w.openAbout(w.guest); len(left) != 0 {
		t.Fatalf("still open: %+v", left)
	}
	closed, err := w.service.List(w.ctx, w.admin, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range closed {
		if report.ID == target {
			if report.Status != reports.StatusActioned || report.Note != "Warned." || report.ResolvedAt == nil {
				t.Fatalf("closed report = %+v", report)
			}
			return
		}
	}
	t.Fatal("the closed report is missing")
}

func TestEveryWayOfPointingAtAHostOrGuestFindsThem(t *testing.T) {
	cases := map[string]func(*world) (uuid.UUID, reports.Submission, uuid.UUID){
		"host from the listing": func(w *world) (uuid.UUID, reports.Submission, uuid.UUID) {
			return w.guest, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonScam, ListingID: w.listing}, w.host
		},
		"host from messages": func(w *world) (uuid.UUID, reports.Submission, uuid.UUID) {
			return w.guest, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonScam, ConversationID: w.conversation}, w.host
		},
		"guest from the booking": func(w *world) (uuid.UUID, reports.Submission, uuid.UUID) {
			return w.host, reports.Submission{Target: reports.TargetGuest, Reason: reports.ReasonScam, BookingID: w.booking}, w.guest
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			reporter, submission, subject := build(w)
			if err := w.file(reporter, submission); err != nil {
				t.Fatal(err)
			}
			if about := w.openAbout(subject); len(about) != 1 {
				t.Fatalf("reports = %+v", about)
			}
		})
	}
}

func TestPeopleCanOnlyReportThoseTheyHaveDealtWith(t *testing.T) {
	w := newWorld(t)
	stranger := w.account()
	cases := map[string]struct {
		reporter   uuid.UUID
		submission reports.Submission
		want       error
	}{
		"stranger with a booking id":        {stranger, reports.Submission{Target: reports.TargetGuest, Reason: reports.ReasonScam, BookingID: w.booking}, reports.ErrTargetNotFound},
		"stranger with a conversation id":   {stranger, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonScam, ConversationID: w.conversation}, reports.ErrTargetNotFound},
		"guest reporting themself as guest": {w.guest, reports.Submission{Target: reports.TargetGuest, Reason: reports.ReasonScam, BookingID: w.booking}, reports.ErrTargetNotFound},
		"host reporting as the guest side":  {w.host, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonScam, BookingID: w.booking}, reports.ErrTargetNotFound},
		"own listing":                       {w.host, reports.Submission{Target: reports.TargetListing, Reason: reports.ReasonScam, ListingID: w.listing}, reports.ErrOwnReport},
		"missing listing":                   {stranger, reports.Submission{Target: reports.TargetListing, Reason: reports.ReasonScam, ListingID: uuid.New()}, reports.ErrTargetNotFound},
	}
	for name, test := range cases {
		if err := w.file(test.reporter, test.submission); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
}

func TestASecondOpenReportIsRefusedUntilTheFirstIsClosed(t *testing.T) {
	w := newWorld(t)
	submission := reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonScam, BookingID: w.booking}
	if err := w.file(w.guest, submission); err != nil {
		t.Fatal(err)
	}
	again := reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonUnsafe, ConversationID: w.conversation}
	if err := w.file(w.guest, again); !errors.Is(err, reports.ErrAlreadyReported) {
		t.Fatalf("second report: %v", err)
	}
	if err := w.service.Resolve(w.ctx, w.admin, w.openAbout(w.host)[0].ID, reports.Decision{Outcome: reports.StatusDismissed}); err != nil {
		t.Fatal(err)
	}
	if err := w.file(w.guest, again); err != nil {
		t.Fatalf("after closing: %v", err)
	}
}

func TestTheDatabaseHidesReportsFromTheReportedPersonAndStrangers(t *testing.T) {
	w := newWorld(t)
	if err := w.file(w.guest, reports.Submission{Target: reports.TargetListing, Reason: reports.ReasonScam, ListingID: w.listing}); err != nil {
		t.Fatal(err)
	}
	report := w.openAbout(w.host)[0].ID
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, w.host, w.host.String()+"@example.test")
	t.Cleanup(func() {
		_, _ = w.superuser.Exec(context.Background(), `DELETE FROM app.admin_accounts WHERE account_id = $1`, w.host)
	})
	for name, test := range map[string]struct {
		viewer uuid.UUID
		want   int
	}{
		"reporter":                 {w.guest, 1},
		"reported, even as admin":  {w.host, 0},
		"stranger":                 {w.account(), 0},
		"an admin who is not them": {w.admin, 1},
	} {
		var seen int
		err := db.WithUserTx(w.ctx, w.pool, test.viewer.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(w.ctx, `SELECT count(*) FROM app.user_reports WHERE id = $1`, report).Scan(&seen)
		})
		if err != nil || seen != test.want {
			t.Errorf("%s sees %d (%v), want %d", name, seen, err, test.want)
		}
	}
	if err := w.service.Resolve(w.ctx, w.host, report, reports.Decision{Outcome: reports.StatusDismissed}); !errors.Is(err, reports.ErrReportNotFound) {
		t.Fatalf("the reported admin closed it: %v", err)
	}
}

func TestTheDatabaseRefusesAForgedReport(t *testing.T) {
	w := newWorld(t)
	stranger := w.account()
	err := db.WithUserTx(w.ctx, w.pool, stranger.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(w.ctx,
			`INSERT INTO app.user_reports
			   (id, reporter_id, subject_id, target, listing_id, booking_id, listing_title, reporter_name, subject_name, reason, details)
			 VALUES ($1, $2, $3, 'guest', $4, $5, 'Report House', 'Nobody', 'Gil Guest', 'scam', 'Made up.')`,
			uuid.New(), stranger, w.guest, w.listing, w.booking)
		return err
	})
	if err == nil {
		t.Fatal("a stranger filed a report against a guest they never hosted")
	}
}

func (w *world) queueAlerts() []notifications.TeamAlert {
	w.t.Helper()
	queued, err := notifications.NewPostgresNotificationStore(w.pool).QueueTeamAlerts(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return queued
}

func (w *world) notices(recipient, report uuid.UUID) int {
	w.t.Helper()
	var count int
	if err := w.superuser.QueryRow(w.ctx,
		`SELECT count(*) FROM app.notifications WHERE account_id = $1 AND event_key = $2`,
		recipient, "user_report_filed:"+report.String()).Scan(&count); err != nil {
		w.t.Fatal(err)
	}
	return count
}

func TestTheTeamIsToldOnceAboutANewReportWithoutItsDetails(t *testing.T) {
	w := newWorld(t)
	if err := w.file(w.guest, reports.Submission{Target: reports.TargetHost, Reason: reports.ReasonHarassment, BookingID: w.booking}); err != nil {
		t.Fatal(err)
	}
	report := w.openAbout(w.host)[0].ID

	var told []notifications.TeamAlert
	for _, alert := range w.queueAlerts() {
		if alert.Recipient == w.admin && alert.Kind == "user_report_filed" && alert.Body == "Review the report about a host at Report House." {
			told = append(told, alert)
		}
	}
	if len(told) == 0 || !told[0].ForTeam || told[0].Title != "A new safety report" {
		t.Fatalf("alerts = %+v", told)
	}
	w.queueAlerts()
	if count := w.notices(w.admin, report); count != 1 {
		t.Fatalf("the admin was told %d times", count)
	}
	if count := w.notices(w.guest, report); count != 0 {
		t.Fatalf("a member received the team alert %d times", count)
	}

	w.sql(`UPDATE app.notifications SET emailed_at = now()
	       WHERE emailed_at IS NULL AND NOT (account_id = $1 AND event_key IS NOT DISTINCT FROM $2)`,
		w.admin, "user_report_filed:"+report.String())
	waiting, err := notifications.NewPostgresNotificationStore(w.pool).AwaitingEmail(w.ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range waiting {
		if email.AccountID == w.admin && email.Kind == "user_report_filed" {
			if email.TeamEmail != w.admin.String()+"@example.test" {
				t.Fatalf("email = %+v", email)
			}
			return
		}
	}
	t.Fatal("no email is waiting for the team")
}

func TestAReportedAdminIsNotToldAboutTheirOwnReport(t *testing.T) {
	w := newWorld(t)
	w.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, w.host, w.host.String()+"@example.test")
	t.Cleanup(func() {
		_, _ = w.superuser.Exec(context.Background(), `DELETE FROM app.admin_accounts WHERE account_id = $1`, w.host)
	})
	if err := w.file(w.guest, reports.Submission{Target: reports.TargetListing, Reason: reports.ReasonScam, ListingID: w.listing}); err != nil {
		t.Fatal(err)
	}
	report := w.openAbout(w.host)[0].ID
	w.queueAlerts()
	if count := w.notices(w.host, report); count != 0 {
		t.Fatalf("the reported admin was told %d times", count)
	}
	if count := w.notices(w.admin, report); count != 1 {
		t.Fatalf("the other admin was told %d times", count)
	}
}

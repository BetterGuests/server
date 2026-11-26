package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresNotificationStore struct{ DB *pgxpool.Pool }

func NewPostgresNotificationStore(pool *pgxpool.Pool) *PostgresNotificationStore {
	return &PostgresNotificationStore{DB: pool}
}

func (store *PostgresNotificationStore) List(ctx context.Context, actorID uuid.UUID, limit int) (Inbox, error) {
	inbox := Inbox{Notifications: []Notification{}}
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*)::integer FROM app.notifications WHERE account_id = $1 AND read_at IS NULL`,
			actorID,
		).Scan(&inbox.UnreadCount); err != nil {
			return fmt.Errorf("count unread: %w", err)
		}
		rows, err := tx.Query(ctx,
			`SELECT n.id, COALESCE(n.booking_id, '00000000-0000-0000-0000-000000000000'::uuid),
			        CASE WHEN n.for_team THEN 'team' WHEN b.guest_id = n.account_id THEN 'guest' ELSE 'host' END,
			        n.kind, n.title, n.body, n.created_at, n.read_at
			 FROM app.notifications n
			 LEFT JOIN app.bookings b ON b.id = n.booking_id
			 WHERE n.account_id = $1
			 ORDER BY n.created_at DESC, n.id DESC
			 LIMIT $2`, actorID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Notification
			if err := rows.Scan(&item.ID, &item.BookingID, &item.Role, &item.Kind, &item.Title, &item.Body, &item.CreatedAt, &item.ReadAt); err != nil {
				return err
			}
			inbox.Notifications = append(inbox.Notifications, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Inbox{}, fmt.Errorf("list notifications: %w", err)
	}
	return inbox, nil
}

func (store *PostgresNotificationStore) MarkRead(ctx context.Context, actorID, notificationID uuid.UUID, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE app.notifications SET read_at = COALESCE(read_at, $3) WHERE id = $1 AND account_id = $2`,
			notificationID, actorID, at)
		if err != nil {
			return fmt.Errorf("mark notification read: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotificationNotFound
		}
		return nil
	})
}

func (store *PostgresNotificationStore) MarkAllRead(ctx context.Context, actorID uuid.UUID, at time.Time) error {
	return db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE app.notifications SET read_at = $2 WHERE account_id = $1 AND read_at IS NULL`,
			actorID, at,
		); err != nil {
			return fmt.Errorf("mark notifications read: %w", err)
		}
		return nil
	})
}

func (store *PostgresNotificationStore) Journeys(ctx context.Context, after uuid.UUID, limit int) ([]Journey, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT booking_id, listing_title, status, payment_status, guest_id, host_id, guest_name,
		        starts_on, ends_on, check_in_time, check_out_time, time_zone,
		        completed_at, checked_in_at, checked_out_at, has_arrival_guide,
		        guest_reviewed, host_reviewed, guest_review_published, host_review_published,
		        sent_notifications, posted_events,
		        cancelled_by, cancellation_refund_cents, report_status, report_refund_cents
		 FROM app.stay_journey($1, $2)`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query journeys: %w", err)
	}
	defer rows.Close()
	var journeys []Journey
	for rows.Next() {
		var j Journey
		if err := rows.Scan(
			&j.BookingID, &j.ListingTitle, &j.Status, &j.PaymentStatus, &j.GuestID, &j.HostID, &j.GuestName,
			&j.StartsOn, &j.EndsOn, &j.CheckInTime, &j.CheckOutTime, &j.TimeZone,
			&j.CompletedAt, &j.CheckedInAt, &j.CheckedOutAt, &j.HasArrivalGuide,
			&j.GuestReviewed, &j.HostReviewed, &j.GuestReviewPublished, &j.HostReviewPublished,
			&j.SentNotifications, &j.PostedEvents,
			&j.CancelledBy, &j.CancelRefundCents, &j.ReportStatus, &j.ReportRefundCents,
		); err != nil {
			return nil, fmt.Errorf("scan journey: %w", err)
		}
		journeys = append(journeys, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read journeys: %w", err)
	}
	return journeys, nil
}

func (store *PostgresNotificationStore) DeliverAlert(ctx context.Context, bookingID, notificationID uuid.UUID, alert Alert) (bool, error) {
	var fresh bool
	if err := store.DB.QueryRow(ctx,
		`SELECT app.deliver_stay_notification($1, $2, $3, $4, $5, $6)`,
		bookingID, string(alert.Kind), alert.Recipient, notificationID, alert.Title, alert.Body,
	).Scan(&fresh); err != nil {
		return false, fmt.Errorf("deliver notification: %w", err)
	}
	return fresh, nil
}

func (store *PostgresNotificationStore) PostNote(ctx context.Context, bookingID, messageID, conversationID uuid.UUID, note ThreadNote) (Posted, bool, error) {
	var posted Posted
	err := store.DB.QueryRow(ctx,
		`SELECT conversation_id, guest_id, host_id, created_at
		 FROM app.post_stay_message($1, $2, $3, $4, $5)`,
		bookingID, string(note.Kind), messageID, conversationID, note.Body,
	).Scan(&posted.ConversationID, &posted.GuestID, &posted.HostID, &posted.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Posted{}, false, nil
	}
	if err != nil {
		return Posted{}, false, fmt.Errorf("post stay message: %w", err)
	}
	return posted, true, nil
}

func (store *PostgresNotificationStore) QueueTeamAlerts(ctx context.Context) ([]TeamAlert, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT account_id, COALESCE(booking_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, title, body, for_team
		 FROM app.queue_team_alerts()`)
	if err != nil {
		return nil, fmt.Errorf("queue team alerts: %w", err)
	}
	alerts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TeamAlert, error) {
		var alert TeamAlert
		err := row.Scan(&alert.Recipient, &alert.BookingID, &alert.Kind, &alert.Title, &alert.Body, &alert.ForTeam)
		return alert, err
	})
	if err != nil {
		return nil, fmt.Errorf("queue team alerts: %w", err)
	}
	return alerts, nil
}

func (store *PostgresNotificationStore) AwaitingEmail(ctx context.Context, limit int) ([]PendingEmail, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT notification_id, account_id, COALESCE(booking_id, '00000000-0000-0000-0000-000000000000'::uuid),
		        kind, title, body, for_team, COALESCE(team_email, '')
		 FROM app.notifications_awaiting_email($1)`, limit)
	if err != nil {
		return nil, fmt.Errorf("query waiting email: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingEmail, error) {
		var item PendingEmail
		err := row.Scan(&item.NotificationID, &item.AccountID, &item.BookingID, &item.Kind,
			&item.Title, &item.Body, &item.ForTeam, &item.TeamEmail)
		return item, err
	})
}

func (store *PostgresNotificationStore) RecordEmailAttempt(ctx context.Context, notificationID uuid.UUID, delivered bool) error {
	if _, err := store.DB.Exec(ctx, `SELECT app.record_email_attempt($1, $2)`, notificationID, delivered); err != nil {
		return fmt.Errorf("record email attempt: %w", err)
	}
	return nil
}

package payments

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

type PostgresPaymentStore struct{ DB *pgxpool.Pool }

func NewPostgresPaymentStore(pool *pgxpool.Pool) *PostgresPaymentStore {
	return &PostgresPaymentStore{DB: pool}
}

func (store *PostgresPaymentStore) QuoteBooking(ctx context.Context, guestID, bookingID uuid.UUID) (Quote, error) {
	var quote Quote
	err := db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT b.id, b.listing_id, l.host_id, b.status, (b.ends_on - b.starts_on), COALESCE(l.nightly_price_cents, 0), b.starts_on
			 FROM app.bookings b
			 JOIN app.listings l ON l.id = b.listing_id
			 WHERE b.id = $1 AND b.guest_id = $2`,
			bookingID, guestID,
		).Scan(&quote.BookingID, &quote.ListingID, &quote.HostID, &quote.BookingStatus, &quote.Nights, &quote.NightlyPriceCents, &quote.StartsOn)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Quote{}, ErrBookingNotFound
	}
	if err != nil {
		return Quote{}, fmt.Errorf("quote booking: %w", err)
	}
	return quote, nil
}

func (store *PostgresPaymentStore) ExistingPayment(ctx context.Context, guestID, bookingID uuid.UUID) (Payment, bool, error) {
	var payment Payment
	err := db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, booking_id, guest_id, stripe_payment_intent_id, amount_cents, currency, status, created_at, updated_at
			 FROM app.payments WHERE booking_id = $1 AND guest_id = $2`,
			bookingID, guestID,
		).Scan(&payment.ID, &payment.BookingID, &payment.GuestID, &payment.StripePaymentIntentID,
			&payment.AmountCents, &payment.Currency, &payment.Status, &payment.CreatedAt, &payment.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, fmt.Errorf("select payment: %w", err)
	}
	return payment, true, nil
}

func (store *PostgresPaymentStore) CreatePayment(ctx context.Context, payment Payment) error {
	return db.WithUserTx(ctx, store.DB, payment.GuestID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, guest_fee_cents, currency, status, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 ON CONFLICT (booking_id) DO UPDATE
			 SET stripe_payment_intent_id = EXCLUDED.stripe_payment_intent_id,
			     amount_cents = EXCLUDED.amount_cents,
			     guest_fee_cents = EXCLUDED.guest_fee_cents,
			     status = 'pending',
			     last_event_at = NULL,
			     updated_at = EXCLUDED.updated_at
			 WHERE app.payments.status <> 'succeeded'`,
			payment.ID, payment.BookingID, payment.GuestID, payment.HostID, payment.StripePaymentIntentID,
			payment.AmountCents, payment.GuestFeeCents, payment.Currency, string(payment.Status), payment.CreatedAt, payment.UpdatedAt)
		if err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}
		return nil
	})
}

func (store *PostgresPaymentStore) RecordDispute(ctx context.Context, dispute Dispute, occurredAt time.Time) (bool, error) {
	var matched bool
	err := store.DB.QueryRow(ctx,
		`SELECT app.record_payment_dispute($1, $2, $3, $4, $5, $6)`,
		dispute.ID, dispute.IntentID, dispute.Status, dispute.AmountCents, dispute.Reason, occurredAt,
	).Scan(&matched)
	if err != nil {
		return false, fmt.Errorf("record dispute: %w", err)
	}
	return matched, nil
}

func (store *PostgresPaymentStore) ApplyOutcome(ctx context.Context, guestID uuid.UUID, intentID string, status Status, occurredAt time.Time) error {
	return db.WithUserTx(ctx, store.DB, guestID.String(), func(tx pgx.Tx) error {
		var bookingID uuid.UUID
		err := tx.QueryRow(ctx,
			`UPDATE app.payments
			 SET status = $3, last_event_at = $4, updated_at = now()
			 WHERE guest_id = $1 AND stripe_payment_intent_id = $2
			   AND status <> 'succeeded'
			   AND (last_event_at IS NULL OR last_event_at <= $4)
			 RETURNING booking_id`,
			guestID, intentID, string(status), occurredAt,
		).Scan(&bookingID)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM app.payments WHERE guest_id = $1 AND stripe_payment_intent_id = $2)`,
				guestID, intentID).Scan(&exists); err != nil {
				return fmt.Errorf("look up payment: %w", err)
			}
			if !exists {
				return ErrPaymentNotFound
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("update payment: %w", err)
		}
		if status != StatusSucceeded {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.bookings SET status = 'confirmed' WHERE id = $1 AND guest_id = $2 AND status = 'pending'`,
			bookingID, guestID); err != nil {
			return fmt.Errorf("confirm booking: %w", err)
		}
		return nil
	})
}

func (store *PostgresPaymentStore) HoldForBooking(ctx context.Context, actorID, bookingID uuid.UUID) (PaymentHold, bool, error) {
	var hold PaymentHold
	err := db.WithUserTx(ctx, store.DB, actorID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT stripe_payment_intent_id, status FROM app.payments
			 WHERE booking_id = $1 AND (guest_id = $2 OR host_id = $2)`,
			bookingID, actorID).Scan(&hold.IntentID, &hold.Status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PaymentHold{}, false, nil
	}
	if err != nil {
		return PaymentHold{}, false, fmt.Errorf("select payment hold: %w", err)
	}
	return hold, true, nil
}

func (store *PostgresPaymentStore) CustomerID(ctx context.Context, accountID uuid.UUID) (string, bool, error) {
	var customerID string
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT stripe_customer_id FROM app.stripe_customers WHERE account_id = $1`, accountID,
		).Scan(&customerID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select stripe customer: %w", err)
	}
	return customerID, true, nil
}

func (store *PostgresPaymentStore) SaveCustomerID(ctx context.Context, accountID uuid.UUID, customerID string) (string, error) {
	var saved string
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, accountID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.stripe_customers (account_id, stripe_customer_id) VALUES ($1, $2)
			 ON CONFLICT (account_id) DO NOTHING`, accountID, customerID); err != nil {
			return fmt.Errorf("insert stripe customer: %w", err)
		}
		return tx.QueryRow(ctx,
			`SELECT stripe_customer_id FROM app.stripe_customers WHERE account_id = $1`, accountID,
		).Scan(&saved)
	})
	if err != nil {
		return "", fmt.Errorf("save stripe customer: %w", err)
	}
	return saved, nil
}

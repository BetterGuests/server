package settlements

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

type PostgresSettlementStore struct{ DB *pgxpool.Pool }

func NewPostgresSettlementStore(pool *pgxpool.Pool) *PostgresSettlementStore {
	return &PostgresSettlementStore{DB: pool}
}

func (store *PostgresSettlementStore) ListDue(ctx context.Context, earliestEndsOn, latestEndsOn time.Time, batchSize int) ([]Due, error) {
	rows, err := store.DB.Query(ctx,
		`SELECT booking_id, guest_id, host_id, payment_intent_id, amount_cents, currency, accepted_task_keys,
		        earlier_refund_cents, host_stripe_account_id, settlement_id, fee_cents, cashback_cents,
		        refund_due_cents, payout_cents, stripe_refund_id, stripe_transfer_id, adjustment_cents, guest_fee_cents,
		        app.settlement_chore_plan(booking_id)
		 FROM app.bookings_due_for_settlement($1, $2, $3)`,
		earliestEndsOn, latestEndsOn, batchSize)
	if err != nil {
		return nil, fmt.Errorf("query due settlements: %w", err)
	}
	defer rows.Close()
	var due []Due
	for rows.Next() {
		var booking Due
		var settlementID *uuid.UUID
		var fee, total, refundDue, payout *int
		var refundID, transferID *string
		if err := rows.Scan(&booking.BookingID, &booking.GuestID, &booking.HostID, &booking.PaymentIntentID,
			&booking.AmountCents, &booking.Currency, &booking.AcceptedTaskKeys, &booking.EarlierRefundCents,
			&booking.HostAccountID, &settlementID, &fee, &total, &refundDue, &payout, &refundID, &transferID, &booking.AdjustmentCents, &booking.GuestFeeCents, &booking.ChorePlan); err != nil {
			return nil, fmt.Errorf("scan due settlement: %w", err)
		}
		if settlementID != nil {
			booking.Saved = &Settlement{
				ID: *settlementID, BookingID: booking.BookingID, GuestID: booking.GuestID, HostID: booking.HostID,
				FeeCents: *fee, CashbackCents: *total, RefundDueCents: *refundDue, PayoutCents: *payout,
				RefundID: textOrEmpty(refundID), TransferID: textOrEmpty(transferID),
			}
		}
		due = append(due, booking)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read due settlements: %w", err)
	}
	return due, nil
}

func (store *PostgresSettlementStore) Save(ctx context.Context, settlement Settlement) error {
	return db.WithUserTx(ctx, store.DB, settlement.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO app.booking_settlements
			     (id, booking_id, guest_id, host_id, fee_cents, cashback_cents, refund_due_cents, payout_cents,
			      stripe_refund_id, stripe_transfer_id, status, reason)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), NULLIF($10, ''), $11, NULLIF($12, ''))
			 ON CONFLICT (booking_id) DO UPDATE
			 SET stripe_refund_id = COALESCE(app.booking_settlements.stripe_refund_id, EXCLUDED.stripe_refund_id),
			     stripe_transfer_id = COALESCE(app.booking_settlements.stripe_transfer_id, EXCLUDED.stripe_transfer_id),
			     status = EXCLUDED.status, reason = EXCLUDED.reason, updated_at = now()
			 WHERE app.booking_settlements.status <> 'settled'`,
			settlement.ID, settlement.BookingID, settlement.GuestID, settlement.HostID, settlement.FeeCents,
			settlement.CashbackCents, settlement.RefundDueCents, settlement.PayoutCents, settlement.RefundID,
			settlement.TransferID, string(settlement.Status), settlement.Reason)
		if err != nil {
			return fmt.Errorf("upsert settlement: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return errors.New("settlement is already settled")
		}
		return nil
	})
}

func (store *PostgresSettlementStore) HostAccount(ctx context.Context, hostID uuid.UUID) (string, bool, error) {
	var accountID string
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT stripe_account_id FROM app.host_payout_accounts WHERE account_id = $1`, hostID,
		).Scan(&accountID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select host payout account: %w", err)
	}
	return accountID, true, nil
}

func (store *PostgresSettlementStore) SaveHostAccount(ctx context.Context, hostID uuid.UUID, accountID string) (string, error) {
	var saved string
	err := db.WithUserTx(ctx, store.DB, hostID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, hostID); err != nil {
			return fmt.Errorf("ensure account: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO app.host_payout_accounts (account_id, stripe_account_id) VALUES ($1, $2)
			 ON CONFLICT (account_id) DO NOTHING`, hostID, accountID); err != nil {
			return fmt.Errorf("insert host payout account: %w", err)
		}
		return tx.QueryRow(ctx,
			`SELECT stripe_account_id FROM app.host_payout_accounts WHERE account_id = $1`, hostID,
		).Scan(&saved)
	})
	if err != nil {
		return "", fmt.Errorf("save host payout account: %w", err)
	}
	return saved, nil
}

func (store *PostgresSettlementStore) IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var admin bool
	err := db.WithUserTx(ctx, store.DB, accountID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.is_admin()`).Scan(&admin)
	})
	if err != nil {
		return false, fmt.Errorf("check admin: %w", err)
	}
	return admin, nil
}

func (store *PostgresSettlementStore) ListBlocked(ctx context.Context, adminID uuid.UUID, limit int) ([]BlockedSettlement, error) {
	blocked := []BlockedSettlement{}
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT booking_id, listing_title, host_name, ends_on, payout_cents, cashback_cents, currency, reason,
			        blocked_at, can_retry
			 FROM app.blocked_settlements($1)`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item BlockedSettlement
			var endsOn time.Time
			if err := rows.Scan(&item.BookingID, &item.ListingTitle, &item.HostName, &endsOn, &item.PayoutCents,
				&item.CashbackCents, &item.Currency, &item.Reason, &item.BlockedAt, &item.CanRetry); err != nil {
				return err
			}
			item.EndsOn = endsOn.Format(time.DateOnly)
			blocked = append(blocked, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list blocked settlements: %w", err)
	}
	return blocked, nil
}

func (store *PostgresSettlementStore) Retry(ctx context.Context, adminID, bookingID uuid.UUID) (bool, error) {
	var released bool
	err := db.WithUserTx(ctx, store.DB, adminID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.retry_blocked_settlement($1)`, bookingID).Scan(&released)
	})
	if err != nil {
		return false, fmt.Errorf("retry blocked settlement: %w", err)
	}
	return released, nil
}

func textOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

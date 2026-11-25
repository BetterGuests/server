package settlements

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/google/uuid"
)

const (
	defaultFeePercent = 12
	maxFeePercent     = 50
	defaultInterval   = time.Hour
	minInterval       = time.Minute
	defaultBatch      = 50
	maxBatch          = 500
	payoutsPagePath   = "/hosting/payouts"
	percent           = 100
)

const (
	reasonDisputed       = "the payment is disputed"
	reasonWrongCapture   = "the captured amount does not match the booking payment"
	reasonOutsideRefund  = "the payment was refunded outside settlement"
	reasonRefundFailed   = "the cashback refund failed"
	reasonRefundMismatch = "an earlier settlement refund has a different amount"
)

var ErrOnboardingDisabled = errors.New("host payouts are not set up")

type SettlementService struct {
	store   Store
	gateway Gateway
	config  Config
	now     func() time.Time
	newID   func() uuid.UUID
}

func NewSettlementService(store Store, gateway Gateway, config Config) *SettlementService {
	return &SettlementService{store: store, gateway: gateway, config: config, now: time.Now, newID: uuid.New}
}

func ConfigFromEnv() (Config, error) {
	return configFromLookup(func(name string) string { return strings.TrimSpace(os.Getenv(name)) })
}

func configFromLookup(lookup func(string) string) (Config, error) {
	config := Config{FeePercent: defaultFeePercent, Interval: defaultInterval, BatchSize: defaultBatch}
	if value := lookup("SETTLEMENT_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("SETTLEMENT_ENABLED must be true or false")
		}
		config.Enabled = enabled
	}
	if value := lookup("PLATFORM_FEE_PERCENT"); value != "" {
		fee, err := strconv.Atoi(value)
		if err != nil || fee < 0 || fee > maxFeePercent {
			return Config{}, fmt.Errorf("PLATFORM_FEE_PERCENT must be a whole number from 0 to %d", maxFeePercent)
		}
		config.FeePercent = fee
	}
	if value := lookup("SETTLEMENT_STARTS_ON"); value != "" {
		startsOn, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return Config{}, errors.New("SETTLEMENT_STARTS_ON must be a date like 2026-12-01")
		}
		config.StartsOn = startsOn
	}
	if config.Enabled && config.StartsOn.IsZero() {
		return Config{}, errors.New("SETTLEMENT_STARTS_ON is required when settlement is enabled, so older stays are never paid out by surprise")
	}
	if value := lookup("SETTLEMENT_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil || interval < minInterval {
			return Config{}, fmt.Errorf("SETTLEMENT_INTERVAL must be a duration of at least %s", minInterval)
		}
		config.Interval = interval
	}
	if value := lookup("SETTLEMENT_BATCH"); value != "" {
		batch, err := strconv.Atoi(value)
		if err != nil || batch < 1 || batch > maxBatch {
			return Config{}, fmt.Errorf("SETTLEMENT_BATCH must be from 1 to %d", maxBatch)
		}
		config.BatchSize = batch
	}
	if value := lookup("PAYOUTS_WEB_URL"); value != "" {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "https" && parsed.Hostname() != "localhost") || parsed.Host == "" {
			return Config{}, errors.New("PAYOUTS_WEB_URL must be an https address, or localhost")
		}
		config.WebURL = strings.TrimRight(value, "/")
	}
	return config, nil
}

func (service *SettlementService) Run(ctx context.Context) {
	if !service.config.Enabled {
		slog.Warn("Settlement is off: no cashback refunds or host payouts will be sent")
		return
	}
	ticker := time.NewTicker(service.config.Interval)
	defer ticker.Stop()
	for {
		report, err := service.SettleDue(ctx)
		if err != nil {
			slog.Error("Settlement run", "error", err)
		} else if report.Due > 0 {
			slog.Info("Settlement run", "due", report.Due, "settled", report.Settled, "waiting", report.Waiting, "blocked", report.Blocked, "failed", report.Failed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (service *SettlementService) SettleDue(ctx context.Context) (Report, error) {
	latest := cashback.LatestSettleableEndsOn(service.now())
	due, err := service.store.ListDue(ctx, service.config.StartsOn, latest, service.config.BatchSize)
	if err != nil {
		return Report{}, fmt.Errorf("list due settlements: %w", err)
	}
	report := Report{Due: len(due)}
	for _, booking := range due {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		status, err := service.settle(ctx, booking)
		switch {
		case err != nil:
			report.Failed++
			slog.Error("Settle booking", "booking_id", booking.BookingID, "error", err)
		case status == StatusSettled:
			report.Settled++
		case status == StatusBlocked:
			report.Blocked++
			slog.Error("Settlement needs a person", "booking_id", booking.BookingID)
		default:
			report.Waiting++
		}
	}
	return report, nil
}

func (service *SettlementService) plan(due Due) Settlement {
	if due.Saved != nil {
		return *due.Saved
	}
	earned := cashback.EarnableCents(due.AcceptedTaskKeys, due.AmountCents)
	total := max(earned, due.EarlierRefundCents)
	fee := due.AmountCents * service.config.FeePercent / percent
	return Settlement{
		ID:             service.newID(),
		BookingID:      due.BookingID,
		GuestID:        due.GuestID,
		HostID:         due.HostID,
		FeeCents:       fee,
		CashbackCents:  total,
		RefundDueCents: total - due.EarlierRefundCents,
		PayoutCents:    max(due.AmountCents-fee-total, 0),
	}
}

func blockReason(due Due, settlement Settlement, charge Charge) string {
	switch {
	case charge.Disputed:
		return reasonDisputed
	case charge.CapturedCents != due.AmountCents:
		return reasonWrongCapture
	case charge.SettlementRefundDead:
		return reasonRefundFailed
	case charge.RefundedCents-charge.SettlementRefundCents > due.EarlierRefundCents:
		return reasonOutsideRefund
	case charge.SettlementRefundID != "" && charge.SettlementRefundCents != settlement.RefundDueCents:
		return reasonRefundMismatch
	}
	return ""
}

func (service *SettlementService) settle(ctx context.Context, due Due) (Status, error) {
	charge, err := service.gateway.Charge(ctx, due.PaymentIntentID, due.BookingID)
	if err != nil {
		return "", fmt.Errorf("read charge: %w", err)
	}
	settlement := service.plan(due)
	if reason := blockReason(due, settlement, charge); reason != "" {
		return service.record(ctx, settlement, StatusBlocked, reason)
	}
	if _, err := service.record(ctx, settlement, StatusProcessing, ""); err != nil {
		return "", err
	}

	if settlement.RefundDueCents > 0 && settlement.RefundID == "" {
		settlement.RefundID = charge.SettlementRefundID
		if settlement.RefundID == "" {
			settlement.RefundID, err = service.gateway.Refund(ctx, RefundRequest{
				BookingID:       due.BookingID,
				PaymentIntentID: due.PaymentIntentID,
				AmountCents:     settlement.RefundDueCents,
			})
			if err != nil {
				return "", fmt.Errorf("refund cashback: %w", err)
			}
		}
		if _, err := service.record(ctx, settlement, StatusProcessing, ""); err != nil {
			return "", err
		}
	}

	if settlement.PayoutCents > 0 && settlement.TransferID == "" {
		transferID, found, err := service.gateway.FindTransfer(ctx, due.BookingID)
		if err != nil {
			return "", fmt.Errorf("look up transfer: %w", err)
		}
		if !found {
			ready, err := service.hostReady(ctx, due.HostAccountID)
			if err != nil {
				return "", err
			}
			if !ready {
				return service.record(ctx, settlement, StatusAwaitingHost, "")
			}
			transferID, err = service.gateway.Transfer(ctx, TransferRequest{
				BookingID:   due.BookingID,
				ChargeID:    charge.ID,
				Destination: due.HostAccountID,
				AmountCents: settlement.PayoutCents,
				Currency:    due.Currency,
			})
			if err != nil {
				return "", fmt.Errorf("pay host: %w", err)
			}
		}
		settlement.TransferID = transferID
	}
	return service.record(ctx, settlement, StatusSettled, "")
}

func (service *SettlementService) hostReady(ctx context.Context, accountID string) (bool, error) {
	if accountID == "" {
		return false, nil
	}
	ready, err := service.gateway.TransfersActive(ctx, accountID)
	if err != nil {
		return false, fmt.Errorf("check host account: %w", err)
	}
	return ready, nil
}

func (service *SettlementService) record(ctx context.Context, settlement Settlement, status Status, reason string) (Status, error) {
	settlement.Status = status
	settlement.Reason = reason
	if err := service.store.Save(ctx, settlement); err != nil {
		return "", fmt.Errorf("record settlement: %w", err)
	}
	return status, nil
}

type PayoutService struct {
	accounts  AccountStore
	onboarder Onboarder
	emails    ContactEmails
	config    Config
}

func NewPayoutService(accounts AccountStore, onboarder Onboarder, emails ContactEmails, config Config) *PayoutService {
	return &PayoutService{accounts: accounts, onboarder: onboarder, emails: emails, config: config}
}

func (service *PayoutService) Status(ctx context.Context, hostID uuid.UUID) (PayoutStatus, error) {
	status := PayoutStatus{FeePercent: service.config.FeePercent}
	accountID, found, err := service.accounts.HostAccount(ctx, hostID)
	if err != nil || !found {
		return status, err
	}
	status.HasAccount = true
	status.Ready, err = service.onboarder.TransfersActive(ctx, accountID)
	if err != nil {
		return PayoutStatus{}, fmt.Errorf("check host account: %w", err)
	}
	return status, nil
}

func (service *PayoutService) StartOnboarding(ctx context.Context, hostID uuid.UUID) (string, error) {
	if service.config.WebURL == "" {
		return "", ErrOnboardingDisabled
	}
	accountID, found, err := service.accounts.HostAccount(ctx, hostID)
	if err != nil {
		return "", err
	}
	if !found {
		email, err := service.emails.VerifiedEmail(ctx, hostID)
		if err != nil {
			return "", fmt.Errorf("find host contact email: %w", err)
		}
		created, err := service.onboarder.CreateAccount(ctx, hostID, email)
		if err != nil {
			return "", fmt.Errorf("create host account: %w", err)
		}
		accountID, err = service.accounts.SaveHostAccount(ctx, hostID, created)
		if err != nil {
			return "", err
		}
	}
	page := service.config.WebURL + payoutsPagePath
	link, err := service.onboarder.OnboardingLink(ctx, accountID, page, page+"?refresh=1")
	if err != nil {
		return "", fmt.Errorf("create onboarding link: %w", err)
	}
	return link, nil
}

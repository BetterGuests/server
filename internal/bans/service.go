package bans

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidBan      = errors.New("give a reason of 500 characters or fewer")
	ErrCannotBanAdmin  = errors.New("you cannot ban yourself or another admin")
	ErrAccountNotFound = errors.New("we could not find that account")
	ErrAlreadyBanned   = errors.New("this account is already banned")
	ErrBanNotFound     = errors.New("that ban is not active")
	ErrNotAdmin        = errors.New("not found")
)

type BanService struct {
	store       Store
	admins      AdminChecker
	connections Disconnector
	now         func() time.Time
	newID       func() uuid.UUID
}

func NewBanService(store Store, admins AdminChecker, connections Disconnector) *BanService {
	return &BanService{store: store, admins: admins, connections: connections, now: time.Now, newID: uuid.New}
}

func (service *BanService) Ban(ctx context.Context, adminID uuid.UUID, request Request) (uuid.UUID, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return uuid.Nil, err
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || utf8.RuneCountInString(request.Reason) > MaxReasonRunes || request.AccountID == uuid.Nil {
		return uuid.Nil, ErrInvalidBan
	}
	targetIsAdmin, err := service.admins.IsAdmin(ctx, request.AccountID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("check target admin: %w", err)
	}
	if request.AccountID == adminID || targetIsAdmin {
		return uuid.Nil, ErrCannotBanAdmin
	}
	banID := service.newID()
	if err := service.store.Ban(ctx, adminID, banID, request, service.now().UTC()); err != nil {
		return uuid.Nil, fmt.Errorf("ban account: %w", err)
	}
	service.connections.Disconnect(request.AccountID)
	slog.Info("Account banned", "account_id", request.AccountID, "admin_id", adminID)
	return banID, nil
}

func (service *BanService) Lift(ctx context.Context, adminID, banID uuid.UUID) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	if err := service.store.Lift(ctx, adminID, banID, service.now().UTC()); err != nil {
		return fmt.Errorf("lift ban: %w", err)
	}
	slog.Info("Ban lifted", "ban_id", banID, "admin_id", adminID)
	return nil
}

func (service *BanService) List(ctx context.Context, adminID uuid.UUID) ([]Ban, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	found, err := service.store.List(ctx, adminID, listLimit)
	if err != nil {
		return nil, fmt.Errorf("list bans: %w", err)
	}
	return found, nil
}

func (service *BanService) requireAdmin(ctx context.Context, accountID uuid.UUID) error {
	admin, err := service.admins.IsAdmin(ctx, accountID)
	if err != nil {
		return fmt.Errorf("check admin: %w", err)
	}
	if !admin {
		return ErrNotAdmin
	}
	return nil
}

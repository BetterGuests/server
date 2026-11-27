package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrBanned          = errors.New("your account is suspended")
)

type AuthService struct {
	verifier    TokenVerifier
	provisioner AccountProvisioner
	bans        BanChecker
}

func NewAuthService(verifier TokenVerifier, provisioner AccountProvisioner) *AuthService {
	return &AuthService{verifier: verifier, provisioner: provisioner}
}

func (service *AuthService) WithBans(bans BanChecker) *AuthService {
	service.bans = bans
	return service
}

func (service *AuthService) Authenticate(ctx context.Context, authorization string) (uuid.UUID, error) {
	rawToken, err := bearerToken(authorization)
	if err != nil {
		return uuid.Nil, ErrUnauthenticated
	}

	identity, err := service.verifier.VerifyIDToken(ctx, rawToken)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if identity.UserID == "" {
		return uuid.Nil, ErrUnauthenticated
	}

	accountID, err := service.provisioner.EnsureAccount(ctx, identity.UserID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("provision account: %w", err)
	}
	if err := service.refuseBanned(ctx, accountID); err != nil {
		return uuid.Nil, err
	}
	return accountID, nil
}

func (service *AuthService) refuseBanned(ctx context.Context, accountID uuid.UUID) error {
	if service.bans == nil {
		return nil
	}
	banned, err := service.bans.IsBanned(ctx, accountID)
	if err != nil {
		return fmt.Errorf("check ban: %w", err)
	}
	if banned {
		return ErrBanned
	}
	return nil
}

func bearerToken(authorization string) (string, error) {
	scheme, token, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", ErrUnauthenticated
	}
	return token, nil
}

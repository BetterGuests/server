package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"

	"github.com/google/uuid"
)

var (
	ErrCardArtUnavailable = errors.New("card art is not set up")
	ErrArtCardMismatch    = errors.New("that is not the same card")
)

var vaultCardID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type CollectSession struct {
	VaultID     string `json:"vault_id"`
	Environment string `json:"environment"`
	AccessToken string `json:"access_token"`
}

type VaultCard struct {
	Last4        string
	ExpMonth     int
	ShortExpYear int
	ArtURL       string
}

type CardArtVault interface {
	CollectSession(ctx context.Context) (CollectSession, error)
	Card(ctx context.Context, vaultCardID string) (VaultCard, error)
	ProvisionToken(ctx context.Context, vaultCardID string) error
	DeleteCard(ctx context.Context, vaultCardID string) error
}

func (service *PaymentService) WithCardArt(vault CardArtVault) *PaymentService {
	service.art = vault
	return service
}

func (service *PaymentService) CardArtEnabled() bool {
	return service.art != nil
}

func (service *PaymentService) StartCardArt(ctx context.Context, accountID uuid.UUID, cardID string) (CollectSession, error) {
	if service.art == nil {
		return CollectSession{}, ErrCardArtUnavailable
	}
	if _, err := service.ownedCard(ctx, accountID, cardID); err != nil {
		return CollectSession{}, err
	}
	return service.art.CollectSession(ctx)
}

func (service *PaymentService) LinkCardArt(ctx context.Context, accountID uuid.UUID, cardID, artCardID string) error {
	if service.art == nil {
		return ErrCardArtUnavailable
	}
	card, err := service.ownedCard(ctx, accountID, cardID)
	if err != nil {
		return err
	}
	if !vaultCardID.MatchString(artCardID) {
		return ErrArtCardMismatch
	}
	found, err := service.art.Card(ctx, artCardID)
	if err != nil {
		return fmt.Errorf("read vault card: %w", err)
	}
	if !samePlasticCard(card, found) {
		return ErrArtCardMismatch
	}
	if err := service.wallet.LinkArtCard(ctx, cardID, artCardID); err != nil {
		return err
	}
	if err := service.art.ProvisionToken(ctx, artCardID); err != nil {
		slog.Warn("Network token was not provisioned", "error", err)
	}
	return nil
}

func samePlasticCard(card SavedCard, found VaultCard) bool {
	return card.Last4 == found.Last4 &&
		card.ExpMonth == found.ExpMonth &&
		card.ExpYear%100 == found.ShortExpYear
}

func (service *PaymentService) ownedCard(ctx context.Context, accountID uuid.UUID, cardID string) (SavedCard, error) {
	cards, err := service.listCards(ctx, accountID)
	if err != nil {
		return SavedCard{}, err
	}
	index := slices.IndexFunc(cards, func(card SavedCard) bool { return card.ID == cardID })
	if index < 0 {
		return SavedCard{}, ErrCardNotFound
	}
	return cards[index], nil
}

func (service *PaymentService) addArt(ctx context.Context, cards []SavedCard) {
	if service.art == nil {
		return
	}
	var pending sync.WaitGroup
	for index := range cards {
		if cards[index].ArtCardID == "" {
			continue
		}
		pending.Add(1)
		go func(card *SavedCard) {
			defer pending.Done()
			found, err := service.art.Card(ctx, card.ArtCardID)
			if err != nil {
				slog.Warn("Card art is unavailable", "error", err)
				return
			}
			card.ArtURL = found.ArtURL
		}(&cards[index])
	}
	pending.Wait()
}

func (service *PaymentService) forgetArt(ctx context.Context, artCardID string) {
	if service.art == nil || artCardID == "" {
		return
	}
	if err := service.art.DeleteCard(ctx, artCardID); err != nil {
		slog.Error("Vault card was not deleted", "vault_card_id", artCardID, "error", err)
	}
}

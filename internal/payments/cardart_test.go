package payments

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type artVaultStub struct {
	card        VaultCard
	provisioned []string
	deleted     []string
}

func (stub *artVaultStub) CollectSession(context.Context) (CollectSession, error) {
	return CollectSession{VaultID: "tnt_test", Environment: "sandbox", AccessToken: "token"}, nil
}

func (stub *artVaultStub) Card(context.Context, string) (VaultCard, error) { return stub.card, nil }

func (stub *artVaultStub) ProvisionToken(_ context.Context, id string) error {
	stub.provisioned = append(stub.provisioned, id)
	return nil
}

func (stub *artVaultStub) DeleteCard(_ context.Context, id string) error {
	stub.deleted = append(stub.deleted, id)
	return nil
}

var guestCard = SavedCard{ID: "pm_mine", Last4: "4242", ExpMonth: 4, ExpYear: 2030}

func artService(wallet *walletStub, vault *artVaultStub) *PaymentService {
	return newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).
		WithSavedCards(wallet, &customerStoreStub{existing: "cus_old"}).
		WithCardArt(vault)
}

func TestMatchingVaultCardIsLinkedAndTokenized(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{guestCard}}
	vault := &artVaultStub{card: VaultCard{Last4: "4242", ExpMonth: 4, ShortExpYear: 30}}

	err := artService(wallet, vault).LinkCardArt(context.Background(), uuid.New(), "pm_mine", "CRD123")

	if err != nil || wallet.linked["pm_mine"] != "CRD123" || len(vault.provisioned) != 1 {
		t.Fatalf("err = %v, linked = %v, provisioned = %v", err, wallet.linked, vault.provisioned)
	}
}

func TestADifferentCardCannotBorrowTheArt(t *testing.T) {
	tests := map[string]VaultCard{
		"other number": {Last4: "1111", ExpMonth: 4, ShortExpYear: 30},
		"other month":  {Last4: "4242", ExpMonth: 5, ShortExpYear: 30},
		"other year":   {Last4: "4242", ExpMonth: 4, ShortExpYear: 31},
	}
	for name, found := range tests {
		t.Run(name, func(t *testing.T) {
			wallet := &walletStub{cards: []SavedCard{guestCard}}

			err := artService(wallet, &artVaultStub{card: found}).
				LinkCardArt(context.Background(), uuid.New(), "pm_mine", "CRD123")

			if !errors.Is(err, ErrArtCardMismatch) || len(wallet.linked) != 0 {
				t.Fatalf("err = %v, linked = %v", err, wallet.linked)
			}
		})
	}
}

func TestVaultCardIDsThatCouldChangeThePathAreRejected(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{guestCard}}

	err := artService(wallet, &artVaultStub{}).
		LinkCardArt(context.Background(), uuid.New(), "pm_mine", "../cards")

	if !errors.Is(err, ErrArtCardMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestNoCollectSessionForSomeoneElsesCard(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{guestCard}}

	_, err := artService(wallet, &artVaultStub{}).
		StartCardArt(context.Background(), uuid.New(), "pm_theirs")

	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinkedCardsShowTheirArtAndRemovalDeletesTheVaultCopy(t *testing.T) {
	linked := guestCard
	linked.ArtCardID = "CRD123"
	wallet := &walletStub{cards: []SavedCard{linked}}
	vault := &artVaultStub{card: VaultCard{ArtURL: "https://art.example/card.png"}}
	service := artService(wallet, vault)

	cards, err := service.SavedCards(context.Background(), uuid.New())
	if err != nil || cards[0].ArtURL != "https://art.example/card.png" {
		t.Fatalf("cards = %+v, err = %v", cards, err)
	}
	if err := service.RemoveCard(context.Background(), uuid.New(), "pm_mine"); err != nil {
		t.Fatalf("RemoveCard() error = %v", err)
	}
	if len(vault.deleted) != 1 || vault.deleted[0] != "CRD123" {
		t.Fatalf("deleted = %v", vault.deleted)
	}
}

func TestVGSClientReadsArtFromTheIncludedNetworkToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"abc"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer abc" || r.URL.Path != "/cards/CRD123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"attributes":{"last4":"4242","exp_month":4,"exp_year":30}},
			"included":[{"type":"network_tokens","meta":{"card_art":{"assets":[
				{"type":"CARD_SYMBOL","download_url":"https://art.example/symbol.png"},
				{"type":"DIGITAL_CARD_ART","download_url":"https://art.example/card.png"}]}}}]}`))
	}))
	defer server.Close()
	client := NewVGSClient(VGSConfig{Environment: "sandbox"})
	client.apiURL, client.tokenURL = server.URL, server.URL+"/token"

	card, err := client.Card(context.Background(), "CRD123")

	want := VaultCard{Last4: "4242", ExpMonth: 4, ShortExpYear: 30, ArtURL: "https://art.example/card.png"}
	if err != nil || card != want {
		t.Fatalf("card = %+v, err = %v", card, err)
	}
}

func TestVGSIsOffWhenUnsetAndRefusesAHalfConfiguration(t *testing.T) {
	_, enabled, err := VGSConfigFromLookup(func(string) string { return "" })
	if enabled || err != nil {
		t.Fatalf("enabled = %v, err = %v", enabled, err)
	}
	_, _, err = VGSConfigFromLookup(func(name string) string {
		if name == "VGS_VAULT_ID" {
			return "tnt_test"
		}
		return ""
	})
	if !errors.Is(err, ErrIncompleteVGSConfig) {
		t.Fatalf("err = %v", err)
	}
}

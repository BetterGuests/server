package listings

import (
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
)

func TestPublishedListingShowsItsHostsAccountNameAndPhoto(t *testing.T) {
	ctx, service := publishFixture(t)
	store := service.store.(*PostgresListingStore)
	names := accounts.NewPostgresAccountStore(store.DB)
	hostID := signedUpHost(t, ctx, service)
	if err := names.Remember(ctx, hostID, "Before B.", ""); err != nil {
		t.Fatal(err)
	}
	listing := draftAt(t, ctx, service, hostID, "1 "+hostID.String()+" Street", "", "85001")
	if err := service.PublishListing(ctx, hostID, listing.ID); err != nil {
		t.Fatal(err)
	}

	assertHost := func(name, avatar string) {
		t.Helper()
		public, err := store.GetPublished(ctx, listing.ID)
		if err != nil {
			t.Fatal(err)
		}
		if public.HostName != name || public.HostAvatarURL != avatar {
			t.Fatalf("host = %q %q, want %q %q", public.HostName, public.HostAvatarURL, name, avatar)
		}
	}
	assertHost("Before B.", "")

	if err := names.Remember(ctx, hostID, "After A.", "https://img.example.com/a.png"); err != nil {
		t.Fatal(err)
	}
	assertHost("After A.", "https://img.example.com/a.png")
}

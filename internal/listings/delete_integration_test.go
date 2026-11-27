package listings

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestHostCanDeleteOnlyAnUntouchedListing(t *testing.T) {
	ctx, service := publishFixture(t)
	draft := func() Listing {
		t.Helper()
		listing, err := service.CreateListing(ctx, signedUpHost(t, ctx, service), CreateListingInput{
			Title: "Delete House", Description: "A complete listing used by the delete integration test.",
			Address: Address{Line1: uuid.NewString()[:8] + " Delete Way", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
			Type:    EntirePlace, PropertyType: House, MaxGuests: 4, MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		})
		if err != nil {
			t.Fatal(err)
		}
		return listing
	}

	t.Run("another account is refused", func(t *testing.T) {
		listing := draft()
		if err := service.DeleteListing(ctx, uuid.New(), listing.ID); !errors.Is(err, ErrNotListingOwner) && !errors.Is(err, ErrListingNotFound) {
			t.Fatalf("stranger delete = %v", err)
		}
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); err != nil {
			t.Fatalf("the host must still be able to delete it: %v", err)
		}
	})

	t.Run("a draft with photos is removed for good", func(t *testing.T) {
		listing := draft()
		priceAndPicture(t, ctx, service, listing)
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); err != nil {
			t.Fatal(err)
		}
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrListingNotFound) {
			t.Fatalf("second delete = %v", err)
		}
	})

	t.Run("a draft with an ownership document takes the document with it", func(t *testing.T) {
		listing := draft()
		approveProof(t, ctx, listing)
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); err != nil {
			t.Fatalf("delete with proof = %v", err)
		}
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrListingNotFound) {
			t.Fatalf("second delete = %v", err)
		}
	})

	t.Run("a published listing nobody has touched is removed for good", func(t *testing.T) {
		listing := draft()
		priceAndPicture(t, ctx, service, listing)
		approveProof(t, ctx, listing)
		if err := service.PublishListing(ctx, listing.HostID, listing.ID); err != nil {
			t.Fatal(err)
		}
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); err != nil {
			t.Fatalf("delete published = %v", err)
		}
		if err := service.DeleteListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrListingNotFound) {
			t.Fatalf("second delete = %v", err)
		}
	})
}

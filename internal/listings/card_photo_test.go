package listings

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"slices"
	"strings"
	"testing"
)

func picture(width, height int) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 5), B: uint8(x + y), A: 255})
		}
	}
	return canvas
}

func jpegOf(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture(width, height), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func TestCardVersionKeepsShapeWithinTheCardEdge(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		wantW, wantH  int
	}{
		{"landscape", 2400, 1600, 960, 640},
		{"portrait", 1500, 2000, 720, 960},
		{"uneven scale", 1001, 667, 960, 640},
		{"very thin", 3000, 2, 960, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source := jpegOf(t, test.width, test.height)

			card := cardVersionOf(source)

			if card == nil {
				if test.name == "very thin" {
					return
				}
				t.Fatal("no card version")
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(card))
			if err != nil || format != "jpeg" || config.Width != test.wantW || config.Height != test.wantH {
				t.Fatalf("%s %dx%d, err %v", format, config.Width, config.Height, err)
			}
			if len(card) >= len(source) {
				t.Fatalf("card is %d bytes, source %d", len(card), len(source))
			}
		})
	}
}

func TestCardVersionAveragesTheSourcePixels(t *testing.T) {
	flat := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for index := 0; index < len(flat.Pix); index += 4 {
		copy(flat.Pix[index:], []byte{200, 100, 50, 255})
	}
	var source bytes.Buffer
	if err := jpeg.Encode(&source, flat, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}

	card, err := jpeg.Decode(bytes.NewReader(cardVersionOf(source.Bytes())))
	if err != nil {
		t.Fatal(err)
	}

	red, green, blue, _ := card.At(480, 240).RGBA()
	for name, off := range map[string]int{"red": int(red>>8) - 200, "green": int(green>>8) - 100, "blue": int(blue>>8) - 50} {
		if off < -4 || off > 4 {
			t.Fatalf("%s drifted by %d", name, off)
		}
	}
}

func TestPhotosThatCannotBeSafelyResizedGetNoCardVersion(t *testing.T) {
	var transparent bytes.Buffer
	if err := png.Encode(&transparent, picture(2000, 1000)); err != nil {
		t.Fatal(err)
	}
	large := jpegOf(t, 2000, 1000)
	rotatedByMetadata := slices.Concat(large[:2], []byte("\xff\xe1\x00\x08Exif\x00\x00"), large[2:])
	declaredHuge := bytes.Clone(large)
	frame := bytes.Index(declaredHuge, []byte("\xff\xc0"))
	copy(declaredHuge[frame+5:], []byte{0xff, 0xff, 0xff, 0xff})

	for name, source := range map[string][]byte{
		"already small":               jpegOf(t, 960, 640),
		"not a JPEG":                  transparent.Bytes(),
		"not an image":                []byte("<html></html>"),
		"orientation kept in EXIF":    rotatedByMetadata,
		"too many pixels to decode":   declaredHuge,
		"cut off after the dimension": large[:len(large)/3],
	} {
		if card := cardVersionOf(source); card != nil {
			t.Errorf("%s: got a %d byte card", name, len(card))
		}
	}
}

func TestACheckedPhotoIsStoredWithItsCardVersion(t *testing.T) {
	for name, screened := range map[string]bool{"screening on": true, "screening off": false} {
		t.Run(name, func(t *testing.T) {
			service, store, objects, listingID := mediaHarness()
			objects.content = jpegOf(t, 2000, 1500)
			if screened {
				service.WithScreening(&screenerStub{})
			}
			upload := ownKey(listingID)

			if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
				t.Fatal(err)
			}

			stored := onlyPhoto(t, store).ObjectKey
			card, sized := cardKeyOf(stored)
			if !sized || stored == upload || !validPhotoKey(stored, listingID) {
				t.Fatalf("stored key = %q", stored)
			}
			if !bytes.Equal(objects.written[stored], objects.content) || len(objects.written[card]) == 0 {
				t.Fatalf("written = %d objects", len(objects.written))
			}
			if !slices.Contains(objects.deleted, upload) {
				t.Fatalf("upload was kept: %v", objects.deleted)
			}
		})
	}
}

func TestASmallPhotoKeepsItsUploadWhenScreeningIsOff(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegOf(t, 400, 300)
	upload := ownKey(listingID)

	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
		t.Fatal(err)
	}

	if onlyPhoto(t, store).ObjectKey != upload || len(objects.written) != 0 || len(objects.deleted) != 0 {
		t.Fatalf("written %v, deleted %v", objects.written, objects.deleted)
	}
}

func TestResendingAResizedUploadSaysItWasAlreadyAdded(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	objects.content = jpegOf(t, 2000, 1500)
	upload := ownKey(listingID)
	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, ""); err != nil {
		t.Fatal(err)
	}

	_, err := service.AddPhoto(context.Background(), store.owner, listingID, upload, "")

	if err != ErrPhotoAlreadyAdded {
		t.Fatalf("err = %v", err)
	}
}

func TestHostsCannotAttachAServerNamedPhotoKey(t *testing.T) {
	service, store, _, listingID := mediaHarness()
	forged := strings.TrimSuffix(ownKey(listingID), ".jpg") + ".sized.jpg"

	if _, err := service.AddPhoto(context.Background(), store.owner, listingID, forged, ""); err != ErrInvalidPhotoKey {
		t.Fatalf("err = %v", err)
	}
}

func TestRemovingAResizedPhotoRemovesBothFiles(t *testing.T) {
	service, store, objects, listingID := mediaHarness()
	store.removed = strings.TrimSuffix(ownKey(listingID), ".jpg") + ".sized.jpg"
	card, _ := cardKeyOf(store.removed)

	if _, err := service.RemovePhoto(context.Background(), store.owner, listingID, listingID); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(objects.deleted, []string{store.removed, card}) {
		t.Fatalf("deleted = %v", objects.deleted)
	}
}

func TestPublicListingsOfferACardLinkOnlyForResizedPhotos(t *testing.T) {
	listing := sampleListing()
	listing.Photos[0].ObjectKey = "listings/a/one.sized.jpg"
	service := NewPublicListingService(&publishedReaderStub{listings: []PublicListing{listing}}, photoSignerStub{})

	result, err := service.ListPublished(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	resized, original := result[0].Photos[0], result[0].Photos[1]
	if !strings.Contains(resized.CardURL, "listings/a/one.card.jpg") || !strings.Contains(resized.URL, "one.sized.jpg") {
		t.Fatalf("resized = %+v", resized)
	}
	if original.CardURL != "" {
		t.Fatalf("original photo got a card link: %q", original.CardURL)
	}
}

package listings

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"slices"
	"testing"
)

func segment(marker byte, body []byte) []byte {
	length := binary.BigEndian.AppendUint16(nil, uint16(len(body)+2))
	return slices.Concat([]byte{0xff, marker}, length, body)
}

func withSegments(source []byte, segments ...[]byte) []byte {
	return slices.Concat(source[:2], slices.Concat(segments...), source[2:])
}

type byteOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

func exifSegment(order byteOrder, tag, value uint16) []byte {
	mark := "MM"
	if order == byteOrder(binary.LittleEndian) {
		mark = "II"
	}
	tiff := order.AppendUint16([]byte(mark), 42)
	tiff = order.AppendUint32(tiff, 8)
	tiff = order.AppendUint16(tiff, 1)
	tiff = order.AppendUint16(tiff, tag)
	tiff = order.AppendUint16(tiff, exifShortType)
	tiff = order.AppendUint32(tiff, 1)
	tiff = order.AppendUint16(tiff, value)
	tiff = order.AppendUint16(tiff, 0)
	tiff = order.AppendUint32(tiff, 0)
	return segment(0xe1, slices.Concat([]byte("Exif\x00\x00"), tiff))
}

func TestOnlyUprightPhotosWithCameraMetadataAreResized(t *testing.T) {
	source := jpegOf(t, 2000, 1000)
	const unrelatedTag = 0x010f
	cases := []struct {
		name    string
		segment []byte
		resized bool
	}{
		{"upright, big endian", exifSegment(binary.BigEndian, exifOrientationTag, 1), true},
		{"upright, little endian", exifSegment(binary.LittleEndian, exifOrientationTag, 1), true},
		{"no orientation recorded", exifSegment(binary.BigEndian, unrelatedTag, 7), true},
		{"turned a quarter", exifSegment(binary.BigEndian, exifOrientationTag, 6), false},
		{"upside down", exifSegment(binary.LittleEndian, exifOrientationTag, 3), false},
		{"unknown value", exifSegment(binary.BigEndian, exifOrientationTag, 0), false},
		{"entries cut off", segment(0xe1, []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x05")), false},
		{"directory beyond the data", segment(0xe1, []byte("Exif\x00\x00MM\x00\x2a\xff\xff\xff\xff")), false},
		{"unknown byte order", segment(0xe1, []byte("Exif\x00\x00XX\x00\x2a\x00\x00\x00\x08\x00\x00")), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			card := cardVersionOf(withSegments(source, test.segment))

			if (card != nil) != test.resized {
				t.Fatalf("resized = %v", card != nil)
			}
			if bytes.Contains(card, []byte("Exif")) {
				t.Fatal("card kept camera metadata")
			}
		})
	}
}

func TestOneTurnedExifBlockIsEnoughToKeepTheOriginal(t *testing.T) {
	source := withSegments(jpegOf(t, 2000, 1000),
		exifSegment(binary.BigEndian, exifOrientationTag, 1),
		exifSegment(binary.BigEndian, exifOrientationTag, 8))

	if cardVersionOf(source) != nil {
		t.Fatal("resized a turned photo")
	}
}

func TestCardVersionKeepsTheColourProfile(t *testing.T) {
	first := segment(0xe2, []byte("ICC_PROFILE\x00\x01\x02first half"))
	second := segment(0xe2, []byte("ICC_PROFILE\x00\x02\x02second half"))
	unrelated := segment(0xe2, []byte("MPF\x00not a colour profile"))

	card := cardVersionOf(withSegments(jpegOf(t, 2000, 1000), first, unrelated, second))

	if !bytes.Contains(card, slices.Concat(first, second)) {
		t.Fatal("colour profile was dropped or reordered")
	}
	if bytes.Contains(card, []byte("MPF")) {
		t.Fatal("unrelated metadata was copied")
	}
	if _, err := jpeg.Decode(bytes.NewReader(card)); err != nil {
		t.Fatal(err)
	}
}

func TestGreyscalePhotosKeepTheOriginal(t *testing.T) {
	grey := image.NewGray(image.Rect(0, 0, 2000, 1000))
	for index := range grey.Pix {
		grey.Pix[index] = byte(index * 7)
	}
	var source bytes.Buffer
	if err := jpeg.Encode(&source, grey, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}

	if cardVersionOf(source.Bytes()) != nil {
		t.Fatal("a greyscale colour profile would not match a colour card")
	}
}

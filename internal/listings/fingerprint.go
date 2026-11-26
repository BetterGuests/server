package listings

import (
	"bytes"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math/bits"
)

type PhotoFingerprint uint64

const (
	fingerprintColumns       = 9
	fingerprintRows          = 8
	fingerprintSamplesPerRow = 12
	maxDuplicateDistance     = 6
	noFingerprint            = PhotoFingerprint(0)
)

func fingerprintOf(data []byte) PhotoFingerprint {
	picture, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return noFingerprint
	}
	bounds := picture.Bounds()
	if bounds.Dx() < fingerprintColumns || bounds.Dy() < fingerprintRows {
		return noFingerprint
	}
	var hash uint64
	for row := range fingerprintRows {
		previous := cellBrightness(picture, bounds, row, 0)
		for column := 1; column < fingerprintColumns; column++ {
			current := cellBrightness(picture, bounds, row, column)
			hash <<= 1
			if previous > current {
				hash |= 1
			}
			previous = current
		}
	}
	return PhotoFingerprint(hash)
}

func cellBrightness(picture image.Image, bounds image.Rectangle, row, column int) int {
	left := bounds.Min.X + column*bounds.Dx()/fingerprintColumns
	right := bounds.Min.X + (column+1)*bounds.Dx()/fingerprintColumns
	top := bounds.Min.Y + row*bounds.Dy()/fingerprintRows
	bottom := bounds.Min.Y + (row+1)*bounds.Dy()/fingerprintRows
	total := 0
	for sampleY := range fingerprintSamplesPerRow {
		y := top + (2*sampleY+1)*(bottom-top)/(2*fingerprintSamplesPerRow)
		for sampleX := range fingerprintSamplesPerRow {
			x := left + (2*sampleX+1)*(right-left)/(2*fingerprintSamplesPerRow)
			total += int(color.GrayModel.Convert(picture.At(x, y)).(color.Gray).Y)
		}
	}
	return total
}

func (hash PhotoFingerprint) sameImageAs(other PhotoFingerprint) bool {
	if hash == noFingerprint || other == noFingerprint {
		return false
	}
	return bits.OnesCount64(uint64(hash^other)) <= maxDuplicateDistance
}

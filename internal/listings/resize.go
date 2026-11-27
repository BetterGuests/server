package listings

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
)

func cardVersionOf(data []byte) []byte {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || bytes.Contains(data, exifMarker) {
		return nil
	}
	if max(config.Width, config.Height) <= cardPhotoEdge || config.Width*config.Height > maxResizedPixels {
		return nil
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	source := image.NewRGBA(image.Rect(0, 0, config.Width, config.Height))
	draw.Draw(source, source.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	longEdge := max(config.Width, config.Height)
	scaled := func(edge int) int { return max(1, (edge*cardPhotoEdge+longEdge/2)/longEdge) }
	small := shrink(source, scaled(config.Width), scaled(config.Height))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, small, &jpeg.Options{Quality: cardPhotoQuality}); err != nil || encoded.Len() >= len(data) {
		return nil
	}
	return encoded.Bytes()
}

func shrink(source *image.RGBA, width, height int) *image.RGBA {
	sourceWidth, sourceHeight := source.Bounds().Dx(), source.Bounds().Dy()
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		top, bottom := y*sourceHeight/height, (y+1)*sourceHeight/height
		for x := range width {
			left, right := x*sourceWidth/width, (x+1)*sourceWidth/width
			averageInto(target.Pix[target.PixOffset(x, y):], source, image.Rect(left, top, right, bottom))
		}
	}
	return target
}

func averageInto(pixel []byte, source *image.RGBA, area image.Rectangle) {
	var sums [4]int
	for y := area.Min.Y; y < area.Max.Y; y++ {
		row := source.Pix[source.PixOffset(area.Min.X, y):source.PixOffset(area.Max.X, y)]
		for index, value := range row {
			sums[index%len(sums)] += int(value)
		}
	}
	count := area.Dx() * area.Dy()
	for channel, sum := range sums {
		pixel[channel] = byte((sum + count/2) / count)
	}
}

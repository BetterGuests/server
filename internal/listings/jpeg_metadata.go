package listings

import (
	"bytes"
	"encoding/binary"
)

func jpegMetadataOf(data []byte) (jpegMetadata, bool) {
	metadata := jpegMetadata{upright: true}
	for at := jpegStartBytes; at+1 < len(data) && data[at] == 0xff; {
		marker := data[at+1]
		if marker == 0xff {
			at++
			continue
		}
		if marker == jpegStartOfScan {
			return metadata, true
		}
		if at+4 > len(data) {
			break
		}
		end := at + 2 + int(binary.BigEndian.Uint16(data[at+2:]))
		if end < at+4 || end > len(data) {
			break
		}
		body := data[at+4 : end]
		if tiff, found := bytes.CutPrefix(body, exifHeader); found && marker == jpegSegmentExif {
			metadata.upright = metadata.upright && exifSaysUpright(tiff)
		}
		if marker == jpegSegmentProfile && bytes.HasPrefix(body, profileHeader) {
			metadata.colourProfile = append(metadata.colourProfile, data[at:end]...)
		}
		at = end
	}
	return jpegMetadata{}, false
}

func exifSaysUpright(tiff []byte) bool {
	if len(tiff) < tiffHeaderBytes {
		return false
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return false
	}
	directory := int(order.Uint32(tiff[4:]))
	if directory < tiffHeaderBytes || directory > len(tiff)-2 {
		return false
	}
	entries := tiff[directory+2:]
	count := int(order.Uint16(tiff[directory:]))
	if count*tiffEntryBytes > len(entries) {
		return false
	}
	for index := range count {
		entry := entries[index*tiffEntryBytes:]
		if order.Uint16(entry) == exifOrientationTag {
			return order.Uint16(entry[2:]) == exifShortType && order.Uint16(entry[8:]) == exifUpright
		}
	}
	return true
}

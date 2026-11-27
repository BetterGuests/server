package proofscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	extractDeadline     = 90 * time.Second
	minUsefulTextLength = 40
	ocrPageLimit        = "3"
	ocrResolution       = "200"
)

var (
	ErrUnsupportedType = errors.New("this file type cannot be read")
	ErrNoText          = errors.New("no text was found in the document")
)

var imageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

type TextExtractor interface {
	Extract(ctx context.Context, data []byte, contentType string) (string, error)
}

type CommandExtractor struct {
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

var requiredTools = []string{"pdftotext", "pdftoppm", "tesseract"}

func MissingTools() []string {
	var missing []string
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	return missing
}

func NewCommandExtractor() *CommandExtractor {
	return &CommandExtractor{run: runTool}
}

func runTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (extractor *CommandExtractor) Extract(ctx context.Context, data []byte, contentType string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, extractDeadline)
	defer cancel()
	workdir, err := os.MkdirTemp("", "proofscan-")
	if err != nil {
		return "", fmt.Errorf("make scratch folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	if contentType == "application/pdf" {
		return extractor.fromPDF(ctx, workdir, data)
	}
	extension, ok := imageExtensions[contentType]
	if !ok {
		return "", ErrUnsupportedType
	}
	image := filepath.Join(workdir, "document"+extension)
	if err := os.WriteFile(image, data, 0o600); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}
	return extractor.ocr(ctx, []string{image})
}

func (extractor *CommandExtractor) fromPDF(ctx context.Context, workdir string, data []byte) (string, error) {
	pdf := filepath.Join(workdir, "document.pdf")
	if err := os.WriteFile(pdf, data, 0o600); err != nil {
		return "", fmt.Errorf("write pdf: %w", err)
	}
	layer, err := extractor.run(ctx, "pdftotext", "-layout", "-l", ocrPageLimit, pdf, "-")
	if err == nil && len(strings.TrimSpace(string(layer))) >= minUsefulTextLength {
		return string(layer), nil
	}
	prefix := filepath.Join(workdir, "page")
	if _, err := extractor.run(ctx, "pdftoppm", "-r", ocrResolution, "-l", ocrPageLimit, "-png", pdf, prefix); err != nil {
		return "", err
	}
	pages, err := filepath.Glob(prefix + "*.png")
	if err != nil || len(pages) == 0 {
		return "", ErrNoText
	}
	sort.Strings(pages)
	return extractor.ocr(ctx, pages)
}

func (extractor *CommandExtractor) ocr(ctx context.Context, images []string) (string, error) {
	var text strings.Builder
	for _, image := range images {
		output, err := extractor.run(ctx, "tesseract", image, "-", "-l", "eng")
		if err != nil {
			return "", err
		}
		text.Write(output)
		text.WriteString("\n")
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", ErrNoText
	}
	return text.String(), nil
}

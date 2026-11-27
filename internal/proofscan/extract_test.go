package proofscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTools struct {
	calls   []string
	outputs map[string]string
	fail    map[string]error
}

func (tools *fakeTools) run(_ context.Context, name string, args ...string) ([]byte, error) {
	tools.calls = append(tools.calls, name)
	if err := tools.fail[name]; err != nil {
		return nil, err
	}
	if name == "pdftoppm" {
		prefix := args[len(args)-1]
		if err := os.WriteFile(prefix+"-1.png", []byte("png"), 0o600); err != nil {
			return nil, err
		}
	}
	return []byte(tools.outputs[name]), nil
}

func extractorWith(tools *fakeTools) *CommandExtractor {
	return &CommandExtractor{run: tools.run}
}

func TestAPDFWithATextLayerSkipsOCR(t *testing.T) {
	tools := &fakeTools{outputs: map[string]string{"pdftotext": utilityBill}}
	text, err := extractorWith(tools).Extract(context.Background(), []byte("%PDF"), "application/pdf")
	if err != nil || text != utilityBill {
		t.Fatalf("text %q, err %v", text, err)
	}
	if strings.Join(tools.calls, ",") != "pdftotext" {
		t.Fatalf("calls %v", tools.calls)
	}
}

func TestAScannedPDFFallsBackToOCR(t *testing.T) {
	tools := &fakeTools{outputs: map[string]string{"pdftotext": "  ", "tesseract": utilityBill}}
	text, err := extractorWith(tools).Extract(context.Background(), []byte("%PDF"), "application/pdf")
	if err != nil || !strings.Contains(text, "COAWETTE") {
		t.Fatalf("text %q, err %v", text, err)
	}
	if strings.Join(tools.calls, ",") != "pdftotext,pdftoppm,tesseract" {
		t.Fatalf("calls %v", tools.calls)
	}
}

func TestAnImageIsReadWithOCR(t *testing.T) {
	tools := &fakeTools{outputs: map[string]string{"tesseract": utilityBill}}
	if _, err := extractorWith(tools).Extract(context.Background(), []byte("jpg"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tools.calls, ",") != "tesseract" {
		t.Fatalf("calls %v", tools.calls)
	}
}

func TestHEICIsNotSupported(t *testing.T) {
	tools := &fakeTools{}
	_, err := extractorWith(tools).Extract(context.Background(), []byte("heic"), "image/heic")
	if !errors.Is(err, ErrUnsupportedType) || len(tools.calls) != 0 {
		t.Fatalf("err %v, calls %v", err, tools.calls)
	}
}

func TestAnImageWithNoTextIsReported(t *testing.T) {
	tools := &fakeTools{outputs: map[string]string{"tesseract": "\n"}}
	_, err := extractorWith(tools).Extract(context.Background(), []byte("png"), "image/png")
	if !errors.Is(err, ErrNoText) {
		t.Fatalf("err %v", err)
	}
}

func TestScratchFilesAreRemovedAfterwards(t *testing.T) {
	var seen string
	tools := &fakeTools{outputs: map[string]string{}}
	extractor := &CommandExtractor{run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		seen = filepath.Dir(args[0])
		return tools.run(ctx, name, args...)
	}}
	_, _ = extractor.Extract(context.Background(), []byte("png"), "image/png")
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("scratch folder %q still exists", seen)
	}
}

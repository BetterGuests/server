package middleware

import (
	"bytes"
	"compress/gzip"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const (
	minCompressedBytes = 1024
	compressionLevel   = 5
)

func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		buffered := &bufferedResponse{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(buffered, r)
		if err := buffered.send(); err != nil {
			slog.Debug("Response was not delivered", "path", r.URL.Path, "error", err)
		}
	})
}

func acceptsGzip(header string) bool {
	for _, offer := range strings.Split(header, ",") {
		name, parameters, _ := strings.Cut(strings.TrimSpace(offer), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		weight, weighted := strings.CutPrefix(strings.TrimSpace(parameters), "q=")
		if !weighted {
			return true
		}
		quality, err := strconv.ParseFloat(weight, 64)
		return err == nil && quality > 0
	}
	return false
}

type bufferedResponse struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (response *bufferedResponse) WriteHeader(status int) {
	response.status = status
}

func (response *bufferedResponse) Write(data []byte) (int, error) {
	return response.body.Write(data)
}

func (response *bufferedResponse) compressible() bool {
	mediaType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
	return err == nil && mediaType == "application/json" &&
		response.Header().Get("Content-Encoding") == "" &&
		response.body.Len() >= minCompressedBytes
}

func (response *bufferedResponse) send() error {
	if !response.compressible() {
		response.ResponseWriter.WriteHeader(response.status)
		if response.body.Len() == 0 {
			return nil
		}
		_, err := response.ResponseWriter.Write(response.body.Bytes())
		return err
	}
	response.Header().Set("Content-Encoding", "gzip")
	response.Header().Del("Content-Length")
	response.ResponseWriter.WriteHeader(response.status)
	compressor, err := gzip.NewWriterLevel(response.ResponseWriter, compressionLevel)
	if err != nil {
		return err
	}
	if _, err := compressor.Write(response.body.Bytes()); err != nil {
		return err
	}
	return compressor.Close()
}

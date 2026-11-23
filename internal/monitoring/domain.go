package monitoring

import "time"

type Config struct {
	DSN         string
	Environment string
	Release     string
}

const flushTimeout = 2 * time.Second

var taggedAttributes = map[string]bool{
	"method": true,
	"path":   true,
	"event":  true,
}

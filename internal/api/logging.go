package api

import (
	"log"
	"sync/atomic"
)

var debugLogging atomic.Bool

// SetDebugLogging enables verbose debug logs for HTTP/control layer.
func SetDebugLogging(enabled bool) {
	debugLogging.Store(enabled)
}

func logDebugf(format string, args ...any) {
	if debugLogging.Load() {
		log.Printf(format, args...)
	}
}

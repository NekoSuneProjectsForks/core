// Package llhls implements the "blocking playlist reload" part of the
// Apple Low-Latency HLS spec: a GET for a .m3u8 that carries the
// _HLS_msn (and optionally _HLS_part) query parameters is held open until
// ffmpeg has written a segment satisfying that request, or a bounded
// timeout elapses, instead of immediately returning a playlist that
// doesn't have it yet.
//
// This only implements the waiting/blocking behaviour. Producing the
// fMP4 segments and #EXT-X-PART entries themselves is an ffmpeg muxer
// concern (hls_segment_type fmp4 and friends), configured on the process
// like any other ffmpeg output option.
package llhls

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/datarhei/core/v16/io/fs"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Config struct {
	// Skipper defines a function to skip the middleware.
	Skipper middleware.Skipper

	// Filesystem the requested playlist is read from to check whether it
	// has advanced far enough to satisfy the request.
	Filesystem fs.Filesystem

	// PollInterval is how often to re-check the playlist while waiting.
	PollInterval time.Duration

	// MaxWait bounds how long a request is held open before falling
	// through to the normal handler (which will serve whatever's
	// currently there, stale or not).
	MaxWait time.Duration
}

var DefaultConfig = Config{
	Skipper: func(c echo.Context) bool {
		return !strings.HasSuffix(c.Request().URL.Path, ".m3u8") || len(c.QueryParam("_HLS_msn")) == 0
	},
	PollInterval: 100 * time.Millisecond,
	MaxWait:      10 * time.Second,
}

// NewWithConfig returns the LL-HLS blocking-reload middleware.
func NewWithConfig(config Config) echo.MiddlewareFunc {
	if config.Skipper == nil {
		config.Skipper = DefaultConfig.Skipper
	}

	if config.PollInterval <= 0 {
		config.PollInterval = DefaultConfig.PollInterval
	}

	if config.MaxWait <= 0 {
		config.MaxWait = DefaultConfig.MaxWait
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if config.Skipper(c) || config.Filesystem == nil {
				return next(c)
			}

			msn, err := strconv.ParseInt(c.QueryParam("_HLS_msn"), 10, 64)
			if err != nil {
				return next(c)
			}

			// If a part is also requested, wait for the segment *after*
			// msn: that guarantees msn itself, and therefore any of its
			// parts, is fully written. This is coarser than tracking
			// individual parts, but correct.
			requireNextSegment := len(c.QueryParam("_HLS_part")) != 0

			wait(c.Request().Context(), config.Filesystem, c.Request().URL.Path, msn, requireNextSegment, config.PollInterval, config.MaxWait)

			return next(c)
		}
	}
}

func wait(ctx context.Context, filesystem fs.Filesystem, path string, msn int64, requireNextSegment bool, pollInterval, maxWait time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		if ready(filesystem, path, msn, requireNextSegment) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ready reports whether the playlist at path has advanced to (or past,
// if requireNextSegment) the requested media sequence number.
func ready(filesystem fs.Filesystem, path string, msn int64, requireNextSegment bool) bool {
	data, err := filesystem.ReadFile(path)
	if err != nil {
		return false
	}

	base, count := parsePlaylist(data)
	if count == 0 {
		return false
	}

	maxMSN := base + count - 1

	if requireNextSegment {
		return maxMSN > msn
	}

	return maxMSN >= msn
}

// parsePlaylist extracts the #EXT-X-MEDIA-SEQUENCE base and counts the
// number of segment entries (#EXTINF) in the playlist.
func parsePlaylist(data []byte) (base, count int64) {
	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		line := scanner.Text()

		if v, ok := strings.CutPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				base = n
			}
		} else if strings.HasPrefix(line, "#EXTINF:") {
			count++
		}
	}

	return base, count
}

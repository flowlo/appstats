package appstats

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

var (
	// ShouldRecord is the function used to determine if recording will occur
	// for a given request. The default is to record all.
	ShouldRecord = RecordRandom(1)

	// ProtoMaxBytes is the amount of protobuf data to record.
	// Data after this is truncated.
	ProtoMaxBytes = 150

	// Namespace is the Memcache namespace under which to store appstats data.
	Namespace = "__appstats__"
)

const (
	serveURL   = "/_ah/stats/"
	detailsURL = serveURL + "details"
	fileURL    = serveURL + "file"
	staticURL  = serveURL + "static/"
)

const (
	statsKey  = "appstats stats"
	headerKey = "appstats header"
)

const bufMaxLen = 1000000

type item struct {
	Key   string
	Value []byte
}

// appStatsContext is here mainly for documentation. Both the appstats.Context
// types for appengine.Context and context.Context implement this.
type _ interface {
	// Used to save the collected statistics to Memcache. The underlying
	// implementation will probably choose to do this via appengine/memcache
	// or google.golang.org/appengine/memcache.
	//
	// setMulti takes a slice of items, because only the way of storing those
	// items is allowed to be implementation specific. The layout of keys
	// and values in Memcache should be consistent.
	setMulti(items []item) error

	// In order to tell the application what's going on, the following to
	// logging helpers are exposed. The underlying implementation will
	// probably be appengine.Context or google.golang.org/appengine/log.
	errorf(format string, a ...interface{})
	infof(format string, a ...interface{})

	header() http.Header
	stats() *requestStats

	URL() string
}

func init() {
	http.HandleFunc(serveURL, appstatsHandler)
}

type responseWriter struct {
	http.ResponseWriter

	ctx Context
}

func (r responseWriter) Write(b []byte) (int, error) {
	stats := r.ctx.stats()

	// Emulate the behavior of http.ResponseWriter.Write since it doesn't
	// call our WriteHeader implementation.
	if stats.Status == 0 {
		r.WriteHeader(http.StatusOK)
	}

	return r.ResponseWriter.Write(b)
}

func (r responseWriter) WriteHeader(status int) {
	stats := r.ctx.stats()

	stats.Status = status
	r.ResponseWriter.WriteHeader(status)
}

// Implements net/http.Handler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ShouldRecord(r) {
		h(appContext(r), w, r)
		return
	}

	ctx := NewContext(r)
	rw := responseWriter{
		ResponseWriter: w,
		ctx:            ctx,
	}
	h(ctx, rw, r)
	save(ctx)
}

// requrl returns the appstats URL for the current request.
func requrl(stats *requestStats) url.URL {
	return url.URL{
		Path:     detailsURL,
		RawQuery: fmt.Sprintf("time=%v", stats.Start.Nanosecond()),
	}
}

func save(ctx Context) error {
	stats := ctx.stats()
	stats.wg.Wait()
	stats.Duration = time.Since(stats.Start)

	// TODO(flowlo): Use a sync.Pool for those buffers ...
	buf := new(bytes.Buffer)
	// ... and encoders.
	enc := gob.NewEncoder(buf)

	full := statsFull{
		Header: ctx.header(),
		Stats:  stats,
	}
	if err := enc.Encode(&full); err != nil {
		ctx.errorf("appstats: save: %v", err)
		return err
	}
	if bufMaxLen > 0 && buf.Len() > bufMaxLen {
		// buf grew too large, so we have to cut stuff down. Stack traces are
		// a good bet. They are strings, so are roughly consistent in length
		// even after encoding and are the most verbose component of the
		// stats.
		overflow := buf.Len() - bufMaxLen
		buf.Reset()

		// NOTE: The following loop operates on the data we threw in gob.Encoder
		// before, so it's not really accurate.
		for i := 0; overflow > 0 && i < len(stats.RPCStats); i++ {
			l := len(stats.RPCStats[i].StackData)
			trunc := min(l, overflow)
			overflow -= trunc
			stats.RPCStats[i].StackData = stats.RPCStats[i].StackData[0 : l-trunc]
		}
		enc.Encode(&full)
	}

	// Save how much we wrote to the buffer, and keep using it.
	n := buf.Len()

	part := statsPart(*stats)
	for i := range part.RPCStats {
		part.RPCStats[i].StackData = ""
		part.RPCStats[i].In = ""
		part.RPCStats[i].Out = ""
	}
	if err := enc.Encode(&part); err != nil {
		ctx.errorf("appstats: save: %v", err)
		return err
	}

	// Get a view of buf's memory.
	b := buf.Bytes()

	itemFull := item{
		Key:   stats.FullKey(),
		Value: b[:n],
	}
	itemPart := item{
		Key:   stats.PartKey(),
		Value: b[n:],
	}

	if err := ctx.setMulti([]item{itemPart, itemFull}); err != nil {
		ctx.errorf("appstats: save: %v", err)
		return err
	}

	ctx.infof("appstats: %s", requrl(stats))
	return nil
}

// RecordRandom constructs a function that will choose to record requests
// with probability p. The actual contents of the request are ignored by
// that function.
func RecordRandom(p float64) func(*http.Request) bool {
	return func(_ *http.Request) bool {
		if p <= 0 {
			return false
		}
		if p >= 1 {
			return true
		}
		return rand.Float64() < p
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

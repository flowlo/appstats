/*
 * Copyright (c) 2013 Matt Jibson <matt.jibson@gmail.com>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package appstats

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"time"

	"golang.org/x/net/context"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/user"

	"github.com/golang/protobuf/proto"
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

var (
	// ShouldRecord is the function used to determine if recording will occur
	// for a given request. The default is to record all.
	ShouldRecord = RecordRandom(1)

	// ProtoMaxBytes is the amount of protobuf data to record.
	// Data after this is truncated.
	ProtoMaxBytes = 150

	// Namespace is the memcache namespace under which to store appstats data.
	Namespace = "__appstats__"

	// Base path for stat URLs. This is pure convenience and can be left blank.
	Base = ""
)

var bufp = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

func init() {
	http.HandleFunc(serveURL, appstatsHandler)
}

type responseWriter struct {
	http.ResponseWriter

	stats *requestStats
}

func (r responseWriter) Write(b []byte) (int, error) {
	// Emulate the behavior of http.ResponseWriter.Write since it doesn't
	// call our WriteHeader implementation.
	if r.stats.Status == 0 {
		r.WriteHeader(http.StatusOK)
	}

	return r.ResponseWriter.Write(b)
}

func (r responseWriter) WriteHeader(status int) {
	r.stats.Status = status
	r.ResponseWriter.WriteHeader(status)
}

// Handler is an http.Handler that records RPC
// statistics if ShouldRecord says so.
type Handler func(ctx context.Context, w http.ResponseWriter, r *http.Request)

// Implements net/http.Handler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ShouldRecord(r) {
		h(appengine.NewContext(r), w, r)
		return
	}

	ctx := NewContext(r)
	rw := responseWriter{
		ResponseWriter: w,
		stats:          stats(ctx),
	}
	h(ctx, rw, r)
	Save(ctx)
}

// URL returns the appstats URL for the given context, i.e. where stats
// can be rendered by a browser in human-readable form.
// See https://cloud.google.com/appengine/docs/python/tools/appstats#Python_A_tour_of_the_Appstats_console
func URL(ctx context.Context) string {
	return rurl(stats(ctx))
}

func rurl(stats *requestStats) string {
	return Base + detailsURL + "?" + fmt.Sprintf("time=%v", stats.Start.Nanosecond())
}

// Save will write collected statistics to memcache. If client code
// is wrapped by a Handler, Save will be called transparently.
func Save(ctx context.Context) error {
	stats := stats(ctx)
	stats.wg.Wait()
	stats.Duration = time.Since(stats.Start)

	buf := bufp.Get().(*bytes.Buffer)
	defer bufp.Put(buf)

	enc := gob.NewEncoder(buf)

	full := statsFull{
		Header: header(ctx),
		Stats:  stats,
	}
	if err := enc.Encode(&full); err != nil {
		log.Errorf(ctx, "appstats: save: %v", err)
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

	n := buf.Len()

	part := statsPart(*stats)
	for i := range part.RPCStats {
		part.RPCStats[i].StackData = ""
		part.RPCStats[i].In = ""
		part.RPCStats[i].Out = ""
	}
	if err := enc.Encode(&part); err != nil {
		log.Errorf(ctx, "appstats: save: %v", err)
		return err
	}

	b := buf.Bytes()

	items := []*memcache.Item{
		&memcache.Item{
			Key:   stats.FullKey(),
			Value: b[:n],
		},
		&memcache.Item{
			Key:   stats.PartKey(),
			Value: b[n:],
		},
	}

	if err := memcache.SetMulti(storeContext(ctx), items); err != nil {
		log.Errorf(ctx, "appstats: save: %v", err)
		return err
	}

	log.Infof(ctx, "appstats: %s", rurl(stats))
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

func stats(ctx context.Context) *requestStats {
	return ctx.Value(statsKey).(*requestStats)
}

func header(ctx context.Context) http.Header {
	return ctx.Value(headerKey).(http.Header)
}

func setup(ctx context.Context, method, path, query string, header http.Header) context.Context {
	stats := &requestStats{
		Method: method,
		Path:   path,
		Query:  query,
		Start:  time.Now(),
	}
	if u := user.Current(ctx); u != nil {
		stats.User = u.String()
		stats.Admin = u.Admin
	}
	ctx = context.WithValue(ctx, statsKey, stats)
	ctx = context.WithValue(ctx, headerKey, header)
	ctx = appengine.WithAPICallFunc(ctx, appengine.APICallFunc(override))
	return ctx
}

// NewContext creates a new timing-aware context from r. It wraps
// appengine.NewContext and then sets up the context for recording
// statistics.
// See https://google.golang.org/appengine#NewConext
func NewContext(r *http.Request) context.Context {
	return setup(
		appengine.NewContext(r),
		r.Method,
		r.URL.Path,
		r.URL.RawQuery,
		r.Header,
	)
}

// WithContext enables profiling of functions without a corresponding request,
// as in the google.golang.org/appengine/delay package. method and path may
// be zero. If ctx is nil, f will be called with nil as argument and WithContext
// is a NOP.
func WithContext(ctx context.Context, method, path string, f func(context.Context)) {
	if ctx == nil {
		f(nil)
		return
	}

	ctx = setup(ctx, method, path, "", nil)

	f(ctx)
	Save(ctx)
}

func storeContext(ctx context.Context) context.Context {
	ctx, err := appengine.Namespace(ctx, Namespace)
	if err != nil {
		// NOTE: appengine.Namespace will only return an error if
		// the passed namespace name itself is invalid.
		panic(err)
	}
	return ctx
}

func override(ctx context.Context, service, method string, in, out proto.Message) error {
	stats := ctx.Value(statsKey).(*requestStats)

	stats.wg.Add(1)
	defer stats.wg.Done()

	if service == "__go__" {
		return appengine.APICall(ctx, service, method, in, out)
	}

	// NOTE: This limits size of stack traces to 65KiB.
	b := make([]byte, 65536)
	i := runtime.Stack(b, false)

	stat := rpcStat{
		Service:   service,
		Method:    method,
		Start:     time.Now(),
		Offset:    time.Since(stats.Start),
		StackData: string(b[0:i]),
	}

	err := appengine.APICall(ctx, service, method, in, out)

	stat.Duration = time.Since(stat.Start)
	stat.In = in.String()
	stat.Out = out.String()
	stat.Cost = getCost(out)

	if len(stat.In) > ProtoMaxBytes {
		stat.In = stat.In[:ProtoMaxBytes] + "..."
	}
	if len(stat.Out) > ProtoMaxBytes {
		stat.Out = stat.Out[:ProtoMaxBytes] + "..."
	}

	stats.lock.Lock()
	stats.RPCStats = append(stats.RPCStats, stat)
	stats.Cost += stat.Cost
	stats.lock.Unlock()
	return err
}

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

// +build !appengine

package appstats

import (
	"net/http"
	"runtime"
	"time"

	"golang.org/x/net/context"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/user"

	"github.com/golang/protobuf/proto"
)

// Handler is an http.Handler that records RPC statistics if ShouldRecord
// says so.
type Handler func(ctx context.Context, w http.ResponseWriter, r *http.Request)

// Context is a timing-aware appengine.Context.
type Context struct {
	context.Context
}

func (ctx Context) errorf(format string, a ...interface{}) {
	log.Errorf(ctx, format, a...)
}

func (ctx Context) infof(format string, a ...interface{}) {
	log.Infof(ctx, format, a...)
}

func (ctx Context) setMulti(items []item) error {
	var conv []*memcache.Item
	for _, it := range items {
		conv = append(conv, &memcache.Item{
			Key:   it.Key,
			Value: it.Value,
		})
	}
	return memcache.SetMulti(ctx, conv)
}

func (ctx Context) stats() *requestStats {
	return ctx.Value(statsKey).(*requestStats)
}

func (ctx Context) header() http.Header {
	return ctx.Value(headerKey).(http.Header)
}

// URL gives the URL to retrieve stats for ctx.
func (ctx Context) URL() string {
	u := requrl(ctx.stats())
	return u.String()
}

func appContext(r *http.Request) context.Context {
	return appengine.NewContext(r)
}

// NewContext creates a new timing-aware context from r. It wraps
// appengine.NewContext and then sets up the context for recording
// statistics.
func NewContext(r *http.Request) Context {
	stats := &requestStats{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Start:  time.Now(),
	}

	ctx := appengine.NewContext(r)

	if u := user.Current(ctx); u != nil {
		stats.User = u.String()
		stats.Admin = u.Admin
	}

	ctx = context.WithValue(ctx, statsKey, stats)
	ctx = context.WithValue(ctx, headerKey, r.Header)
	ctx = appengine.WithAPICallFunc(ctx, appengine.APICallFunc(override))

	return Context{ctx}
}

// WithContext enables profiling of functions without a corresponding request,
// as in the google.golang.org/appengine/delay package. method and path may
// be empty. If ctx is nil, f will be called with nil as argument.
func WithContext(ctx context.Context, method, path string, f func(context.Context)) {
	if ctx == nil {
		f(nil)
		return
	}

	stats := &requestStats{
		Method: method,
		Path:   path,
		Start:  time.Now(),
	}

	if u := user.Current(ctx); u != nil {
		stats.User = u.String()
		stats.Admin = u.Admin
	}

	ctx = context.WithValue(ctx, statsKey, stats)
	ctx = appengine.WithAPICallFunc(ctx, appengine.APICallFunc(override))

	f(ctx)
	save(Context{ctx})
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

	// TODO(flowlo): Somehow wrap the actual RPC call. This is
	// currently not possible, because all the magic is in
	// google.golang.org/appengine/internal
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

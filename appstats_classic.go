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

// +build appengine

package appstats

import (
	"net/http"
	"runtime/debug"
	"time"

	"appengine"
	"appengine/memcache"
	"appengine/user"
	"appengine_internal"
)

// Handler is an http.Handler that records RPC statistics if ShouldRecord
// says so.
type Handler func(ctx appengine.Context, w http.ResponseWriter, r *http.Request)

// Context is a timing-aware appengine.Context.
type Context struct {
	appengine.Context
	h http.Header
	s *requestStats
}

// URL gives the URL to retrieve stats for ctx.
func (ctx Context) URL() string {
	u := requrl(ctx.s)
	return u.String()
}

func (ctx Context) errorf(format string, a ...interface{}) {
	ctx.Errorf(format, a...)
}

func (ctx Context) infof(format string, a ...interface{}) {
	ctx.Infof(format, a...)
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
	return ctx.s
}

func (ctx Context) header() http.Header {
	return ctx.h
}

// Call times an appengine.Context Call. Internal use only.
func (ctx Context) Call(service, method string, in, out appengine_internal.ProtoMessage, opts *appengine_internal.CallOptions) error {
	ctx.s.wg.Add(1)
	defer ctx.s.wg.Done()

	if service == "__go__" {
		return ctx.Context.Call(service, method, in, out, opts)
	}

	stat := rpcStat{
		Service:   service,
		Method:    method,
		Start:     time.Now(),
		Offset:    time.Since(ctx.s.Start),
		StackData: string(debug.Stack()),
	}
	err := ctx.Context.Call(service, method, in, out, opts)
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

	ctx.s.lock.Lock()
	ctx.s.RPCStats = append(ctx.s.RPCStats, stat)
	ctx.s.Cost += stat.Cost
	ctx.s.lock.Unlock()
	return err
}

func appContext(r *http.Request) appengine.Context {
	return appengine.NewContext(r)
}

// NewContext creates a new timing-aware context from r.
func NewContext(r *http.Request) Context {
	ctx := appengine.NewContext(r)
	var uname string
	var admin bool
	if u := user.Current(ctx); u != nil {
		uname = u.String()
		admin = u.Admin
	}
	return Context{
		Context: ctx,
		h:       r.Header,
		s: &requestStats{
			User:   uname,
			Admin:  admin,
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Start:  time.Now(),
		},
	}
}

// WithContext enables profiling of functions without a corresponding request,
// as in the appengine/delay package. method and path may be empty.
func WithContext(ctx appengine.Context, method, path string, f func(Context)) {
	var uname string
	var admin bool
	if u := user.Current(ctx); u != nil {
		uname = u.String()
		admin = u.Admin
	}
	appCtx := Context{
		Context: ctx,
		s: &requestStats{
			User:   uname,
			Admin:  admin,
			Method: method,
			Path:   path,
			Start:  time.Now(),
		},
	}
	f(appCtx)
	save(appCtx)
}

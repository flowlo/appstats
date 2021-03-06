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

/*
Package appstats profiles the RPC performance of Google App Engine applications.

Reference: https://developers.google.com/appengine/docs/python/tools/appstats

To use this package, change your HTTP handler functions to use this signature:

	func(appengine.Context, http.ResponseWriter, *http.Request)

Register them in the usual way, wrapping them with appstats.Handler.


Examples

Using appstats.Handler to wrap your actual handler, appstats will take care of
storing the obtained statsitics. after your handler has finished processing.

	import (
		"net/http"

		"golang.org/x/net/context"

		"github.com/mjibson/appstats"
	)

	func init() {
		http.Handle("/foo", appstats.Handler(foo))
	}

	func foo(ctx context.Context, w http.ResponseWriter, r *http.Request) {
		// do stuff with ctx: datastore.Get(ctx, key, entity)
		w.Write([]byte("foo"))
	}

appstats.NewContext allows exchanging flexibility for respnsibility. You must call
appstats.Save to persist statistics:

	import (
		"net/http"

		"github.com/mjibson/appstats"
	)

	func init() {
		http.HandlerFunc("/foo", http.HandleFunc(foo))
	}

	func foo(w http.ResponseWriter, r *http.Request) {
		ctx := appstats.NewContext(r)
		defer appstats.Save(ctx)
		// do stuff with ctx: datastore.Get(ctx, key, entity)
		w.Write([]byte("foo"))
	}

Usage

Use your app, and view the appstats interface at http://localhost:8080/_ah/stats/, or your production URL.

Configuration

Refer to the variables section of the documentation: http://godoc.org/github.com/mjibson/appstats#pkg-variables.

Routing

In general, your app.yaml will not need to change. In the case of conflicting
routes, add the following to your app.yaml:

	handlers:
	- url: /_ah/stats/.*
	  script: _go_app


TODO

Cost calculation is experimental. Currently it only includes write ops (read and small ops are TODO).
*/
package appstats

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
	"html/template"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/context"

	"google.golang.org/appengine"
	"google.golang.org/appengine/memcache"
	"google.golang.org/appengine/user"
)

var templates *template.Template
var initTime = time.Now()

func init() {
	templates = template.New("appstats").Funcs(funcs)
	templates.Parse(htmlBase)
	templates.Parse(htmlMain)
	templates.Parse(htmlDetails)
	templates.Parse(htmlFile)
}

func serveError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func appstatsHandler(w http.ResponseWriter, r *http.Request) {
	c := storeContext(appengine.NewContext(r))
	if appengine.IsDevAppServer() {
		// noop
	} else if u := user.Current(c); u == nil {
		if loginURL, err := user.LoginURL(c, r.URL.String()); err == nil {
			http.Redirect(w, r, loginURL, http.StatusTemporaryRedirect)
		} else {
			serveError(w, err)
		}
		return
	} else if !u.Admin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if detailsURL == r.URL.Path {
		details(c, w, r)
	} else if fileURL == r.URL.Path {
		file(c, w, r)
	} else if strings.HasPrefix(r.URL.Path, staticURL) {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		content, ok := static[name]
		if !ok {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, content.Sum))
		http.ServeContent(w, r, name, initTime, content)
	} else {
		index(c, w, r)
	}
}

func index(c context.Context, w http.ResponseWriter, r *http.Request) {
	keys := make([]string, modulus)
	for i := range keys {
		keys[i] = fmt.Sprintf(keyPart, i*distance)
	}

	items, err := memcache.GetMulti(c, keys)
	if err != nil {
		return
	}

	ars := allrequestStats{}
	for _, v := range items {
		t := stats_part{}
		err := gob.NewDecoder(bytes.NewBuffer(v.Value)).Decode(&t)
		if err != nil {
			continue
		}
		r := requestStats(t)
		ars = append(ars, &r)
	}
	sort.Sort(reverse{ars})

	requestByID := make(map[int]*requestStats, len(ars))
	idByRequest := make(map[*requestStats]int, len(ars))
	requests := make(map[int]*statByName)
	byRequest := make(map[int]map[string]cVal)
	for i, v := range ars {
		idx := i + 1
		requestByID[idx] = v
		idByRequest[v] = idx
		requests[idx] = &statByName{
			RequestStats: v,
		}
		byRequest[idx] = make(map[string]cVal)
	}

	requestByPath := make(map[string][]int)
	byCount := make(map[string]cVal)
	byRPC := make(map[skey]cVal)
	for _, t := range ars {
		id := idByRequest[t]

		requestByPath[t.Path] = append(requestByPath[t.Path], id)

		for _, r := range t.RPCStats {
			rpc := r.Name()

			v := byRequest[id][rpc]
			v.count++
			v.cost += r.Cost
			byRequest[id][rpc] = v

			v = byCount[rpc]
			v.count++
			v.cost += r.Cost
			byCount[rpc] = v

			v = byRPC[skey{rpc, t.Path}]
			v.count++
			v.cost += r.Cost
			byRPC[skey{rpc, t.Path}] = v
		}
	}

	for k, v := range byRequest {
		stats := statsByName{}
		for rpc, s := range v {
			stats = append(stats, &statByName{
				Name:  rpc,
				Count: s.count,
				Cost:  s.cost,
			})
		}
		sort.Sort(reverse{stats})
		requests[k].SubStats = stats
	}

	statsByRPC := make(map[string]statsByName)
	pathStats := make(map[string]statsByName)
	for k, v := range byRPC {
		statsByRPC[k.a] = append(statsByRPC[k.a], &statByName{
			Name:  k.b,
			Count: v.count,
			Cost:  v.cost,
		})
		pathStats[k.b] = append(pathStats[k.b], &statByName{
			Name:  k.a,
			Count: v.count,
			Cost:  v.cost,
		})
	}
	for k, v := range statsByRPC {
		sort.Sort(reverse{v})
		statsByRPC[k] = v
	}

	pathStatsByCount := statsByName{}
	for k, v := range pathStats {
		total := 0
		var cost int64
		for _, stat := range v {
			total += stat.Count
			cost += stat.Cost
		}
		sort.Sort(reverse{v})

		pathStatsByCount = append(pathStatsByCount, &statByName{
			Name:       k,
			Count:      total,
			Cost:       cost,
			SubStats:   v,
			Requests:   len(requestByPath[k]),
			RecentReqs: requestByPath[k],
		})
	}
	sort.Sort(reverse{pathStatsByCount})

	allStatsByCount := statsByName{}
	for k, v := range byCount {
		allStatsByCount = append(allStatsByCount, &statByName{
			Name:     k,
			Count:    v.count,
			Cost:     v.cost,
			SubStats: statsByRPC[k],
		})
	}
	sort.Sort(reverse{allStatsByCount})

	v := struct {
		Env                 map[string]string
		Requests            map[int]*statByName
		RequestStatsByCount map[int]*statByName
		AllStatsByCount     statsByName
		PathStatsByCount    statsByName
	}{
		Env: map[string]string{
			"APPLICATION_ID": appengine.AppID(c),
		},
		Requests:         requests,
		AllStatsByCount:  allStatsByCount,
		PathStatsByCount: pathStatsByCount,
	}

	_ = templates.ExecuteTemplate(w, "main", v)
}

func details(c context.Context, w http.ResponseWriter, r *http.Request) {
	i, _ := strconv.Atoi(r.FormValue("time"))
	qtime := roundTime(i)
	key := fmt.Sprintf(keyFull, qtime)

	v := struct {
		Env             map[string]string
		Record          *requestStats
		Header          http.Header
		AllStatsByCount statsByName
		Real            time.Duration
	}{
		Env: map[string]string{
			"APPLICATION_ID": appengine.AppID(c),
		},
	}

	item, err := memcache.Get(c, key)
	if err != nil {
		templates.ExecuteTemplate(w, "details", v)
		return
	}

	full := stats_full{}
	err = gob.NewDecoder(bytes.NewBuffer(item.Value)).Decode(&full)
	if err != nil {
		templates.ExecuteTemplate(w, "details", v)
		return
	}

	byCount := make(map[string]cVal)
	durationCount := make(map[string]time.Duration)
	var _real time.Duration
	for _, r := range full.Stats.RPCStats {
		rpc := r.Name()

		// byCount
		if _, present := byCount[rpc]; !present {
			durationCount[rpc] = 0
		}
		v := byCount[rpc]
		v.count++
		v.cost += r.Cost
		byCount[rpc] = v
		durationCount[rpc] += r.Duration
		_real += r.Duration
	}

	allStatsByCount := statsByName{}
	for k, v := range byCount {
		allStatsByCount = append(allStatsByCount, &statByName{
			Name:     k,
			Count:    v.count,
			Cost:     v.cost,
			Duration: durationCount[k],
		})
	}
	sort.Sort(allStatsByCount)

	v.Record = full.Stats
	v.Header = full.Header
	v.AllStatsByCount = allStatsByCount
	v.Real = _real

	_ = templates.ExecuteTemplate(w, "details", v)
}

func file(c context.Context, w http.ResponseWriter, r *http.Request) {
	fname := r.URL.Query().Get("f")
	n := r.URL.Query().Get("n")
	lineno, _ := strconv.Atoi(n)

	f, err := ioutil.ReadFile(fname)
	if err != nil {
		serveError(w, err)
		return
	}

	fp := make(map[int]string)
	for k, v := range strings.Split(string(f), "\n") {
		fp[k+1] = v
	}

	v := struct {
		Env      map[string]string
		Filename string
		Lineno   int
		Fp       map[int]string
	}{
		Env: map[string]string{
			"APPLICATION_ID": appengine.AppID(c),
		},
		Filename: fname,
		Lineno:   lineno,
		Fp:       fp,
	}

	_ = templates.ExecuteTemplate(w, "file", v)
}

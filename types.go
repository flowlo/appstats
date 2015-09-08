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
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	keyPrefix = "__appstats__:"
	keyPart   = keyPrefix + "%06d:part"
	keyFull   = keyPrefix + "%06d:full"
	distance  = 100
	modulus   = 1000
)

type requestStats struct {
	User        string
	Admin       bool
	Method      string
	Path, Query string
	Status      int
	Cost        int64
	Start       time.Time
	Duration    time.Duration
	RPCStats    []rpcStat

	lock sync.Mutex
	wg   sync.WaitGroup
}

type statsPart requestStats

type statsFull struct {
	Header http.Header
	Stats  *requestStats
}

func (r *requestStats) PartKey() string {
	t := roundTime(r.Start.Nanosecond())
	return fmt.Sprintf(keyPart, t)
}

func (r *requestStats) FullKey() string {
	t := roundTime(r.Start.Nanosecond())
	return fmt.Sprintf(keyFull, t)
}

func roundTime(i int) int {
	return (i / 1000 / distance) % modulus * distance
}

type rpcStat struct {
	Service, Method string
	Start           time.Time
	Offset          time.Duration
	Duration        time.Duration
	StackData       string
	In, Out         string
	Cost            int64
}

func (r rpcStat) Name() string {
	return r.Service + "." + r.Method
}

func (r rpcStat) Request() string {
	return r.In
}

func (r rpcStat) Response() string {
	return r.Out
}

func (r rpcStat) Stack() stack {
	lines := strings.Split(r.StackData, "\n")
	if len(lines) < 7 || len(lines)%2 != 0 {
		return stack{}
	}

	// First line contains goroutine index and state,
	// something like "goroutine 1337 [...]:". This is skipped.
	lines = lines[1:]

	// Also, cut the next two entries, as they will be the calls to
	// appengine.APICall and appstats.override every time.
	lines = lines[4:]

	frames := make([]*frame, 0, len(lines)/2)

	for i := 0; i+1 < len(lines); i += 2 {
		// Lines come in pairs, first the function
		// name of the frame with it's arguments, ...
		f := &frame{Call: lines[i]}

		i++

		idx := strings.LastIndex(lines[i], " ")
		cidx := strings.LastIndex(lines[i], ":")
		if idx == -1 || cidx == -1 {
			continue
		}

		f.Location = lines[i][1:cidx]
		f.Lineno, _ = strconv.Atoi(lines[i][cidx:idx])

		frames = append(frames, f)
	}

	return frames
}

type stack []*frame

type frame struct {
	Location string
	Call     string
	Lineno   int
}

type allrequestStats []*requestStats

func (s allrequestStats) Len() int           { return len(s) }
func (s allrequestStats) Less(i, j int) bool { return s[i].Start.Sub(s[j].Start) < 0 }
func (s allrequestStats) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type statsByName []*statByName

func (s statsByName) Len() int           { return len(s) }
func (s statsByName) Less(i, j int) bool { return s[i].Count < s[j].Count }
func (s statsByName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

type statByName struct {
	Name         string
	Count        int
	Cost         int64
	SubStats     []*statByName
	Requests     int
	RecentReqs   []int
	RequestStats *requestStats
	Duration     time.Duration
}

type reverse struct{ sort.Interface }

func (r reverse) Less(i, j int) bool { return r.Interface.Less(j, i) }

type skey struct {
	a, b string
}

type cVal struct {
	count int
	cost  int64
}

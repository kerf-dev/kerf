// Command e2esummary turns `go test -json` output of the e2e suite (test/e2e) into the usual
// verbose log on stdout and, at the end, a per-test duration table plus, for the slowest tests,
// what each of their waits waited for (the suite's "<what> after <duration>" log lines).
//
//	go test -json -tags e2e ./test/e2e/... 2>&1 | go run ./test/e2e/e2esummary -slowest 5
//
// Lines that are not JSON (build errors) are copied through. With -t0 <unix seconds> (the start of
// `make e2e`) it also reports the setup time before the first test started.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type event struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

type result struct {
	Name    string
	Status  string
	Elapsed time.Duration
	Waits   []string
}

// waitLine matches the suite's wait logs: eventually ("<what> after 1m3s"), consistently
// ("<what> held for 45s"), the spoke-2 lock and plain sleeps ("slept 1m0s: <why>").
var waitLine = regexp.MustCompile(`^\s+\S+\.go:\d+: (.+ (after|held for) \d[\dhmsµn.]*|spoke-2 lock taken after .+|slept .+)$`)

type summary struct {
	results  map[string]*result
	first    time.Time // first event
	firstRun time.Time // first test started
	last     time.Time
}

func newSummary() *summary { return &summary{results: map[string]*result{}} }

func (s *summary) get(name string) *result {
	r := s.results[name]
	if r == nil {
		r = &result{Name: name}
		s.results[name] = r
	}
	return r
}

func (s *summary) add(e event) {
	if !e.Time.IsZero() {
		if s.first.IsZero() {
			s.first = e.Time
		}
		s.last = e.Time
	}
	if e.Test == "" {
		return
	}
	switch e.Action {
	case "run":
		if s.firstRun.IsZero() {
			s.firstRun = e.Time
		}
		s.get(e.Test)
	case "pass", "fail", "skip":
		r := s.get(e.Test)
		r.Status = strings.ToUpper(e.Action)
		r.Elapsed = time.Duration(e.Elapsed * float64(time.Second))
	case "output":
		if m := waitLine.FindStringSubmatch(strings.TrimRight(e.Output, "\n")); m != nil {
			r := s.get(e.Test)
			r.Waits = append(r.Waits, m[1])
		}
	}
}

func (s *summary) print(w io.Writer, slowest int, t0 time.Time) {
	var top, all []*result
	for _, r := range s.results {
		all = append(all, r)
		if !strings.Contains(r.Name, "/") {
			top = append(top, r)
		}
	}
	byTime := func(rs []*result) {
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].Elapsed != rs[j].Elapsed {
				return rs[i].Elapsed > rs[j].Elapsed
			}
			return rs[i].Name < rs[j].Name
		})
	}
	byTime(top)
	byTime(all)
	fmt.Fprintln(w, "\n== e2e: per-test durations ==")
	fmt.Fprintf(w, "| %-58s | %-6s | %8s |\n", "Test", "Result", "Seconds")
	fmt.Fprintf(w, "|%s|%s|%s|\n", strings.Repeat("-", 60), strings.Repeat("-", 8), strings.Repeat("-", 10))
	for _, r := range all {
		st := r.Status
		if st == "" {
			st = "?"
		}
		fmt.Fprintf(w, "| %-58s | %-6s | %8.1f |\n", r.Name, st, r.Elapsed.Seconds())
	}
	if !s.first.IsZero() {
		fmt.Fprintf(w, "\ngo test: first event %s, last %s: %s\n", s.first.Format(time.TimeOnly), s.last.Format(time.TimeOnly),
			s.last.Sub(s.first).Round(time.Second))
	}
	if !t0.IsZero() && !s.firstRun.IsZero() {
		fmt.Fprintf(w, "setup (make e2e start -> first test started): %s; wall (make e2e start -> last result): %s\n",
			s.firstRun.Sub(t0).Round(time.Second), s.last.Sub(t0).Round(time.Second))
	}
	if slowest > len(top) {
		slowest = len(top)
	}
	for _, r := range top[:slowest] {
		fmt.Fprintf(w, "\n-- %s (%.0fs): waits --\n", r.Name, r.Elapsed.Seconds())
		names := []string{r.Name}
		for _, sub := range all {
			if strings.HasPrefix(sub.Name, r.Name+"/") {
				names = append(names, sub.Name)
			}
		}
		for _, n := range names {
			for _, w2 := range s.results[n].Waits {
				prefix := ""
				if n != r.Name {
					prefix = strings.TrimPrefix(n, r.Name+"/") + ": "
				}
				fmt.Fprintf(w, "   %s%s\n", prefix, w2)
			}
		}
	}
}

func main() {
	slowest := flag.Int("slowest", 5, "print the waits of the N slowest top-level tests")
	t0s := flag.Int64("t0", 0, "unix time `make e2e` started (setup time)")
	flag.Parse()
	var t0 time.Time
	if *t0s > 0 {
		t0 = time.Unix(*t0s, 0)
	}
	s := newSummary()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	for sc.Scan() {
		line := sc.Bytes()
		var e event
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &e) != nil {
			_, _ = out.Write(line)
			_ = out.WriteByte('\n')
			out.Flush()
			continue
		}
		s.add(e)
		if e.Output != "" {
			_, _ = out.WriteString(e.Output)
			out.Flush()
		}
	}
	s.print(out, *slowest, t0)
	out.Flush()
}

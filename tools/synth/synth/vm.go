package synth

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang/snappy"
)

// VMWriter pushes series to VictoriaMetrics (remote write or /api/v1/import JSON lines) with
// several concurrent connections. Emit is safe for concurrent use.
type VMWriter struct {
	url        string
	client     *http.Client
	tl         Timeline
	batchBytes int
	gzip       bool

	ch      chan *Series
	wg      sync.WaitGroup
	errOnce sync.Once
	err     error
	failed  chan struct{}

	Series, Samples, Bytes atomic.Int64
	// Paused counts how long requests waited for VictoriaMetrics memory to drop.
	Paused atomic.Int64

	rw      bool
	base    string
	maxRSS  float64
	rss     atomic.Int64
	stopMon chan struct{}
}

// VMOptions configure a VMWriter.
type VMOptions struct {
	Workers    int  // concurrent import requests (default 4)
	BatchBytes int  // flush a request at this many bytes (default 8 MiB)
	Gzip       bool // gzip request bodies (json format only)
	// Format is "remote-write" (Prometheus remote write protobuf + snappy to /api/v1/write,
	// default, fastest for VictoriaMetrics to ingest) or "json" (/api/v1/import JSON lines).
	Format string
	// MaxRSSFraction pauses imports while VictoriaMetrics' Go heap in use is above this
	// fraction of its available memory (default 0.75; negative disables). VictoriaMetrics
	// itself keeps caches up to 60% of available memory, so lower values stall the import. The shared dev VM
	// runs with a 768Mi limit and is OOM-killed by unthrottled backfills.
	MaxRSSFraction float64
}

// NewVMWriter starts the import workers. Call Close to flush.
func NewVMWriter(vmURL string, tl Timeline, opt VMOptions) *VMWriter {
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.BatchBytes <= 0 {
		opt.BatchBytes = 8 << 20
	}
	if opt.MaxRSSFraction == 0 {
		opt.MaxRSSFraction = 0.75
	}
	if opt.Format == "" {
		opt.Format = "remote-write"
	}
	v := &VMWriter{
		rw:         opt.Format == "remote-write",
		base:       strings.TrimRight(vmURL, "/"),
		stopMon:    make(chan struct{}),
		url:        strings.TrimRight(vmURL, "/") + map[bool]string{true: "/api/v1/write", false: "/api/v1/import"}[opt.Format == "remote-write"],
		client:     &http.Client{Timeout: 5 * time.Minute},
		tl:         tl,
		batchBytes: opt.BatchBytes,
		gzip:       opt.Gzip,
		ch:         make(chan *Series, 256),
		failed:     make(chan struct{}),
	}
	if opt.MaxRSSFraction > 0 {
		if avail, rss, err := v.memory(); err == nil && avail > 0 {
			v.maxRSS = avail * opt.MaxRSSFraction
			v.rss.Store(int64(rss))
			go v.monitor()
		} else {
			slog.Warn("cannot read VictoriaMetrics memory metrics; importing without memory guard", "err", err)
		}
	}
	for i := 0; i < opt.Workers; i++ {
		v.wg.Add(1)
		go v.worker()
	}
	return v
}

// memory reads vm_available_memory_bytes and go_memstats_heap_inuse_bytes from /metrics.
func (v *VMWriter) memory() (avail, rss float64, err error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(v.base + "/metrics")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch name {
		case "vm_available_memory_bytes":
			avail, _ = strconv.ParseFloat(val, 64)
		case "go_memstats_heap_inuse_bytes":
			// Heap in use, not RSS: Go keeps freed memory resident, so RSS never drops.
			rss, _ = strconv.ParseFloat(val, 64)
		}
	}
	return avail, rss, nil
}

func (v *VMWriter) monitor() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-v.stopMon:
			return
		case <-t.C:
			if _, rss, err := v.memory(); err == nil && rss > 0 {
				v.rss.Store(int64(rss))
			} else {
				v.rss.Store(int64(v.maxRSS) + 1) // unreachable (restarting?): wait
			}
		}
	}
}

// waitMemory blocks while VictoriaMetrics is above the memory threshold.
func (v *VMWriter) waitMemory() {
	if v.maxRSS == 0 {
		return
	}
	start := time.Now()
	for float64(v.rss.Load()) > v.maxRSS {
		time.Sleep(200 * time.Millisecond)
		if time.Since(start) > 5*time.Minute {
			slog.Warn("VictoriaMetrics memory stays above the guard; continuing", "rssMiB", v.rss.Load()>>20)
			break
		}
	}
	v.Paused.Add(int64(time.Since(start)))
}

// Emit queues a series for import.
func (v *VMWriter) Emit(s *Series) error {
	select {
	case v.ch <- s:
		return nil
	case <-v.failed:
		return v.err
	}
}

// Close flushes and waits for all requests.
func (v *VMWriter) Close() error {
	close(v.ch)
	v.wg.Wait()
	close(v.stopMon)
	return v.err
}

func (v *VMWriter) fail(err error) {
	v.errOnce.Do(func() { v.err = err; close(v.failed) })
}

func (v *VMWriter) worker() {
	defer v.wg.Done()
	buf := make([]byte, 0, v.batchBytes+(2<<20))
	for s := range v.ch {
		buf = v.appendLine(buf, s)
		v.Series.Add(1)
		v.Samples.Add(int64(s.Len))
		if len(buf) >= v.batchBytes {
			if err := v.post(buf); err != nil {
				v.fail(err)
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		if err := v.post(buf); err != nil {
			v.fail(err)
		}
	}
}

func (v *VMWriter) post(body []byte) error {
	select {
	case <-v.failed:
		return v.err
	default:
	}
	v.Bytes.Add(int64(len(body)))
	payload := body
	if v.rw {
		payload = snappy.Encode(nil, body)
	} else if v.gzip {
		var zb bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&zb, gzip.BestSpeed)
		zw.Write(body)
		zw.Close()
		payload = zb.Bytes()
	}
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
		v.waitMemory()
		req, err := http.NewRequest(http.MethodPost, v.url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		switch {
		case v.rw:
			req.Header.Set("Content-Encoding", "snappy")
			req.Header.Set("Content-Type", "application/x-protobuf")
			req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
		case v.gzip:
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := v.client.Do(req)
		if err != nil {
			last = err
			slog.Warn("import request failed, retrying", "attempt", attempt+1, "err", err)
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		last = fmt.Errorf("import: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode/100 == 4 {
			return last
		}
		slog.Warn("import request failed, retrying", "attempt", attempt+1, "err", last)
	}
	return last
}

// appendLine encodes one series in VictoriaMetrics JSON-line import format.
func (v *VMWriter) appendLine(b []byte, s *Series) []byte {
	if v.rw {
		return AppendRemoteWrite(b, s, v.tl)
	}
	return AppendJSONLine(b, s, v.tl)
}

// AppendRemoteWrite appends s as one WriteRequest.timeseries entry (field 1) of a Prometheus
// remote-write protobuf message. Concatenated entries form a valid WriteRequest.
func AppendRemoteWrite(b []byte, s *Series, tl Timeline) []byte {
	labelsLen := 0
	for _, l := range s.Labels {
		m := 1 + uvarintLen(uint64(len(l.Name))) + len(l.Name) + 1 + uvarintLen(uint64(len(l.Value))) + len(l.Value)
		labelsLen += 1 + uvarintLen(uint64(m)) + m
	}
	t0, step := tl.Ms(s.Start), tl.Step.Milliseconds()
	samplesLen := 0
	for k := 0; k < s.Len; k++ {
		m := 9 + 1 + uvarintLen(uint64(t0+int64(k)*step))
		samplesLen += 1 + uvarintLen(uint64(m)) + m
	}
	tsLen := labelsLen + samplesLen
	b = append(b, 0x0a) // WriteRequest.timeseries, bytes
	b = binary.AppendUvarint(b, uint64(tsLen))
	for _, l := range s.Labels {
		m := 1 + uvarintLen(uint64(len(l.Name))) + len(l.Name) + 1 + uvarintLen(uint64(len(l.Value))) + len(l.Value)
		b = append(b, 0x0a) // TimeSeries.labels
		b = binary.AppendUvarint(b, uint64(m))
		b = append(b, 0x0a) // Label.name
		b = binary.AppendUvarint(b, uint64(len(l.Name)))
		b = append(b, l.Name...)
		b = append(b, 0x12) // Label.value
		b = binary.AppendUvarint(b, uint64(len(l.Value)))
		b = append(b, l.Value...)
	}
	for k := 0; k < s.Len; k++ {
		ts := uint64(t0 + int64(k)*step)
		m := 9 + 1 + uvarintLen(ts)
		b = append(b, 0x12) // TimeSeries.samples
		b = binary.AppendUvarint(b, uint64(m))
		b = append(b, 0x09) // Sample.value, fixed64
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(s.Value(k)))
		b = append(b, 0x10) // Sample.timestamp, varint
		b = binary.AppendUvarint(b, ts)
	}
	return b
}

func uvarintLen(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

// AppendJSONLine encodes s as {"metric":{...},"values":[...],"timestamps":[...]}\n.
func AppendJSONLine(b []byte, s *Series, tl Timeline) []byte {
	b = append(b, `{"metric":{`...)
	for i, l := range s.Labels {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, l.Name)
		b = append(b, ':')
		b = appendJSONString(b, l.Value)
	}
	b = append(b, `},"values":[`...)
	for k := 0; k < s.Len; k++ {
		if k > 0 {
			b = append(b, ',')
		}
		b = appendFloat(b, s.Value(k))
	}
	b = append(b, `],"timestamps":[`...)
	t0, step := tl.Ms(s.Start), tl.Step.Milliseconds()
	for k := 0; k < s.Len; k++ {
		if k > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, t0+int64(k)*step, 10)
	}
	return append(b, "]}\n"...)
}

func appendFloat(b []byte, f float64) []byte {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.AppendInt(b, int64(f), 10)
	}
	return strconv.AppendFloat(b, f, 'g', 10, 64)
}

func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, fmt.Sprintf(`\u%04x`, c)...)
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

// VMAdmin wraps the VictoriaMetrics admin endpoints used around a backfill.
type VMAdmin struct {
	URL    string
	Client *http.Client
}

func (a VMAdmin) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (a VMAdmin) call(ctx context.Context, method, path string, q url.Values) error {
	u := strings.TrimRight(a.URL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// DeleteClusters deletes every series whose cluster label is one of the given (synth-*) names.
func (a VMAdmin) DeleteClusters(ctx context.Context, clusters []string) error {
	for _, c := range clusters {
		if !strings.HasPrefix(c, "synth-") {
			return fmt.Errorf("refusing to delete non-synthetic cluster %q", c)
		}
	}
	match := fmt.Sprintf(`{cluster=~%q}`, strings.Join(clusters, "|"))
	return a.call(ctx, http.MethodPost, "/api/v1/admin/tsdb/delete_series", url.Values{"match[]": {match}})
}

// Flush makes recently imported data searchable and drops cached query results, which would
// otherwise hide backfilled history.
func (a VMAdmin) Flush(ctx context.Context) error {
	if err := a.call(ctx, http.MethodGet, "/internal/force_flush", nil); err != nil {
		return err
	}
	return a.call(ctx, http.MethodGet, "/internal/resetRollupResultCache", nil)
}

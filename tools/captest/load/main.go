package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	rps     = flag.Int("rps", 200, "target requests/s (open loop)")
	dur     = flag.Duration("dur", 30*time.Second, "duration")
	targets = flag.String("targets", "10.201.0.11:8317", "comma separated proxies")
	stream  = flag.Bool("stream", true, "stream")
	nconv   = flag.Int("conv", 20000, "conversations")
	nkeys   = flag.Int("keys", 10000, "api keys")
	model   = flag.String("model", "gemini-2.5-flash", "model")
	out     = flag.String("out", "", "json result file")
	label   = flag.String("label", "", "label")
	maxIn   = flag.Int("max-inflight", 200000, "cap in-flight")
)

type res struct {
	ttfb, total time.Duration
	code        int
	err         string
}

func pct(d []time.Duration, p float64) float64 {
	if len(d) == 0 {
		return 0
	}
	return float64(d[int(float64(len(d)-1)*p)].Microseconds()) / 1000
}

func main() {
	flag.Parse()
	tg := strings.Split(*targets, ",")
	sys := strings.Repeat("You are a careful assistant. Follow the policy text exactly. ", 50)
	tr := &http.Transport{MaxIdleConns: 0, MaxIdleConnsPerHost: 20000, MaxConnsPerHost: 0, IdleConnTimeout: 90 * time.Second,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, DisableCompression: true}
	cl := &http.Client{Transport: tr, Timeout: 120 * time.Second}
	var inflight, peak, dropped atomic.Int64
	results := make(chan res, 1<<16)
	var all []res
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		for r := range results {
			all = append(all, r)
		}
		close(done)
	}()
	interval := time.Second / time.Duration(*rps)
	start := time.Now()
	n := 0
	for time.Since(start) < *dur {
		next := start.Add(time.Duration(n) * interval)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		n++
		if inflight.Load() >= int64(*maxIn) {
			dropped.Add(1)
			continue
		}
		c := rand.Intn(*nconv)
		key := fmt.Sprintf("capkey-%05d", c%*nkeys)
		turns := 1 + rand.Intn(3)
		msgs := []map[string]string{{"role": "system", "content": fmt.Sprintf("[conv %d] %s", c, sys)}}
		for t := 0; t < turns; t++ {
			msgs = append(msgs, map[string]string{"role": "user", "content": fmt.Sprintf("question %d of conv %d", t, c)})
			if t < turns-1 {
				msgs = append(msgs, map[string]string{"role": "assistant", "content": "ok"})
			}
		}
		body, _ := json.Marshal(map[string]any{"model": *model, "stream": *stream, "messages": msgs, "max_tokens": 200})
		target := tg[n%len(tg)]
		wg.Add(1)
		v := inflight.Add(1)
		for {
			p := peak.Load()
			if v <= p || peak.CompareAndSwap(p, v) {
				break
			}
		}
		go func() {
			defer wg.Done()
			defer inflight.Add(-1)
			t0 := time.Now()
			req, _ := http.NewRequest("POST", "http://"+target+"/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := cl.Do(req)
			if err != nil {
				e := err.Error()
				if len(e) > 60 {
					e = e[len(e)-60:]
				}
				results <- res{total: time.Since(t0), err: e}
				return
			}
			br := bufio.NewReader(resp.Body)
			_, _ = br.Peek(1)
			ttfb := time.Since(t0)
			_, _ = io.Copy(io.Discard, br)
			resp.Body.Close()
			r := res{ttfb: ttfb, total: time.Since(t0), code: resp.StatusCode}
			results <- r
		}()
	}
	sent := time.Since(start)
	wg.Wait()
	close(results)
	<-done
	codes := map[string]int{}
	var ok, ttfb []time.Duration
	for _, r := range all {
		k := fmt.Sprint(r.code)
		if r.err != "" {
			k = "err:" + r.err
		}
		codes[k]++
		if r.code == 200 {
			ok = append(ok, r.total)
			ttfb = append(ttfb, r.ttfb)
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i] < ok[j] })
	sort.Slice(ttfb, func(i, j int) bool { return ttfb[i] < ttfb[j] })
	summary := map[string]any{"label": *label, "target_rps": *rps, "sent": len(all) + int(dropped.Load()), "achieved_rps": float64(len(all)) / sent.Seconds(),
		"ok": len(ok), "codes": codes, "dropped": dropped.Load(), "peak_inflight": peak.Load(),
		"total_ms": map[string]float64{"p50": pct(ok, .5), "p90": pct(ok, .9), "p99": pct(ok, .99), "max": pct(ok, 1)},
		"ttfb_ms": map[string]float64{"p50": pct(ttfb, .5), "p90": pct(ttfb, .9), "p99": pct(ttfb, .99), "max": pct(ttfb, 1)}}
	b, _ := json.Marshal(summary)
	fmt.Println(string(b))
	if *out != "" {
		f, _ := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		f.Write(append(b, '\n'))
		f.Close()
	}
}

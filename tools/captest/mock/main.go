// Mock Gemini upstream: configurable latency, chunking and 429/503 rates.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var (
	latMin  = flag.Duration("lat-min", 5*time.Second, "min total latency")
	latMax  = flag.Duration("lat-max", 30*time.Second, "max total latency")
	chunks  = flag.Int("chunks", 10, "stream chunks")
	p429    = flag.Float64("p429", 0, "429 rate")
	p503    = flag.Float64("p503", 0, "503 rate")
	addr    = flag.String("addr", ":19999", "listen")
	inflight, total, e429, e503 atomic.Int64
)

const usage = `"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":40,"totalTokenCount":1240,"cachedContentTokenCount":800}`

func handler(w http.ResponseWriter, r *http.Request) {
	total.Add(1)
	inflight.Add(1)
	defer inflight.Add(-1)
	x := rand.Float64()
	if x < *p429 {
		e429.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"model":"gemini-2.5-flash"}},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"20s"}]}}`)
		return
	}
	if x < *p429+*p503 {
		e503.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"code":503,"message":"No capacity available for model gemini-2.5-flash on the server","status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`)
		return
	}
	lat := *latMin
	if *latMax > *latMin {
		lat += time.Duration(rand.Int63n(int64(*latMax - *latMin)))
	}
	text := func(i int) string {
		return fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"chunk %d lorem ipsum dolor sit amet "}]},"index":0}]`, i)
	}
	if strings.Contains(r.URL.Path, "streamGenerateContent") {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		step := lat / time.Duration(*chunks)
		for i := 0; i < *chunks; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(step):
			}
			s := text(i)
			if i == *chunks-1 {
				s = strings.Replace(s, `,"index":0}]`, `,"index":0,"finishReason":"STOP"}],`+usage, 1)
			} else {
				s += "}"
			}
			fmt.Fprintf(w, "data: %s\r\n\r\n", s)
			if fl != nil {
				fl.Flush()
			}
		}
		return
	}
	select {
	case <-r.Context().Done():
		return
	case <-time.After(lat):
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, strings.Replace(text(0), `,"index":0}]`, `,"index":0,"finishReason":"STOP"}],`+usage, 1)+"}")
}

func main() {
	flag.Parse()
	http.HandleFunc("/", handler)
	http.HandleFunc("/_stats", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"inflight":%d,"total":%d,"e429":%d,"e503":%d}`, inflight.Load(), total.Load(), e429.Load(), e503.Load())
	})
	srv := &http.Server{Addr: *addr, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

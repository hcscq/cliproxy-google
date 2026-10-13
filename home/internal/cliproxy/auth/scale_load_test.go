package auth

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
)

func setupScale(b testing.TB, n int) *Manager {
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	models := []*registry.ModelInfo{}
	for j := 0; j < 20; j++ {
		models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("m%d", j), Object: "model", Type: "antigravity"})
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("scale-%05d", i)
		reg.RegisterClient(id, "antigravity", models)
		ms := map[string]*ModelState{}
		for j := 0; j < 20; j++ {
			ms[fmt.Sprintf("m%d", j)] = &ModelState{Status: StatusActive, UpdatedAt: time.Now()}
		}
		m.Register(context.Background(), &Auth{ID: id, Provider: "antigravity", Status: StatusActive,
			Metadata: map[string]any{"email": id + "@x.com", "refresh_token": "rt", "access_token": "at"}, ModelStates: ms})
	}
	return m
}

func BenchmarkScaleDispatch(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			m := setupScale(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := m.Dispatch(context.Background(), []string{"antigravity"}, fmt.Sprintf("m%d", i%20), Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestZZLoad(t *testing.T) {
	if os.Getenv("ZZDUR") == "" {
		t.Skip("set ZZDUR=30s to run the 10k-credential load test")
	}
	n := 10000
	m := setupScale(t, n)
	dur := 30 * time.Second
	if s := os.Getenv("ZZDUR"); s != "" {
		d, _ := time.ParseDuration(s)
		dur = d
	}
	var picks sync.Map
	var total, errs atomic.Int64
	var lat atomic.Int64
	stop := time.Now().Add(dur)
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			for time.Now().Before(stop) {
				model := fmt.Sprintf("m%d", r.Intn(20))
				t0 := time.Now()
				d, err := m.Dispatch(context.Background(), []string{"antigravity"}, model, Options{})
				lat.Add(int64(time.Since(t0)))
				total.Add(1)
				if err != nil {
					errs.Add(1)
					continue
				}
				if model == "m0" {
					v, _ := picks.LoadOrStore(d.Auth.ID, new(atomic.Int64))
					v.(*atomic.Int64).Add(1)
				}
				res := Result{AuthID: d.Auth.ID, Provider: "antigravity", Model: model, Success: true}
				x := r.Float64()
				if x < 0.15 {
					ra := time.Duration(1+r.Intn(5)) * time.Second
					res.Success = false
					res.RetryAfter = &ra
					res.Error = &Error{HTTPStatus: 429, Message: "RESOURCE_EXHAUSTED"}
				} else if x < 0.18 {
					res.Success = false
					res.Error = &Error{HTTPStatus: 503, Message: "capacity"}
				}
				m.MarkResult(context.Background(), res)
				time.Sleep(time.Duration(r.Intn(2)) * time.Millisecond)
			}
		}(g)
	}
	var ms runtime.MemStats
	for time.Now().Before(stop) {
		time.Sleep(5 * time.Second)
		runtime.GC()
		runtime.ReadMemStats(&ms)
		if f := os.Getenv("ZZHEAP"); f != "" {
			fh, _ := os.Create(fmt.Sprintf("%s.%d", f, time.Now().Unix()))
			pprof.WriteHeapProfile(fh)
			fh.Close()
		}
		t.Logf("t heap=%dMB sys=%dMB reqs=%d err=%d", ms.HeapAlloc>>20, ms.Sys>>20, total.Load(), errs.Load())
	}
	wg.Wait()
	var vals []float64
	picks.Range(func(_, v any) bool { vals = append(vals, float64(v.(*atomic.Int64).Load())); return true })
	sort.Float64s(vals)
	var sum, sq float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	for _, v := range vals {
		sq += (v - mean) * (v - mean)
	}
	t.Logf("rps=%.0f avgLat=%v errRate=%.3f m0 creds=%d min=%.0f max=%.0f mean=%.1f sd=%.1f",
		float64(total.Load())/dur.Seconds(), time.Duration(lat.Load()/total.Load()), float64(errs.Load())/float64(total.Load()),
		len(vals), vals[0], vals[len(vals)-1], mean, math.Sqrt(sq/float64(len(vals))))
}

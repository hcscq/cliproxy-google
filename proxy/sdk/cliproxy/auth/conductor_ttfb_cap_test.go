package auth

import (
	"errors"
	"net/http"
	"testing"
)

type ttfbTestErr struct {
	code int
	body string
}

func (e ttfbTestErr) Error() string        { return e.body }
func (e ttfbTestErr) StatusCode() int      { return e.code }
func (e ttfbTestErr) ResponseBody() []byte { return []byte(e.body) }

const ttfbBody = `{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"x: no first byte from upstream within 15s"}}`

func runBudget(errs ...error) (bool, string) {
	b := &retryBudget{maxCreds: 5}
	var stop bool
	var cause string
	for i, e := range errs {
		b.noteAttempt(string(rune('a' + i)))
		stop, cause = b.exhausted(e)
		if stop {
			return stop, cause
		}
	}
	return stop, cause
}

func TestFirstByteTimeoutCap(t *testing.T) {
	defer SetMaxFirstByteTimeouts(-1)
	tt := ttfbTestErr{http.StatusGatewayTimeout, ttfbBody}
	other := ttfbTestErr{http.StatusServiceUnavailable, "no capacity"}

	SetMaxFirstByteTimeouts(-1) // default 2
	if stop, cause := runBudget(tt, tt); !stop || cause != "first-byte timeout cap reached" {
		t.Fatalf("default cap: %v %q", stop, cause)
	}
	if stop, _ := runBudget(tt); stop {
		t.Fatal("stopped after one timeout")
	}
	// Other errors keep the old 5-credential behaviour.
	if stop, cause := runBudget(other, other, other, other, other); !stop || cause != "max-request-credentials reached" {
		t.Fatalf("other: %v %q", stop, cause)
	}
	if stop, _ := runBudget(tt, other, other); stop {
		t.Fatal("one timeout + others should continue")
	}
	SetMaxFirstByteTimeouts(0) // unlimited
	if stop, cause := runBudget(tt, tt, tt, tt, tt); !stop || cause != "max-request-credentials reached" {
		t.Fatalf("unlimited: %v %q", stop, cause)
	}
	SetMaxFirstByteTimeouts(3)
	if stop, _ := runBudget(tt, tt); stop {
		t.Fatal("cap 3 stopped at 2")
	}
	if !isFirstByteTimeoutErr(tt) || isFirstByteTimeoutErr(ttfbTestErr{504, "gateway"}) || isFirstByteTimeoutErr(errors.New("no first byte from upstream")) {
		t.Fatal("classifier")
	}
}

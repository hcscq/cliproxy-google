package management

import (
	"net/http"
)

type kimiLoginTransport func(*http.Request) (*http.Response, error)

func (f kimiLoginTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

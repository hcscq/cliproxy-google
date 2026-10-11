package cluster

import "testing"

func TestDSNWithHostPort(t *testing.T) {
	cases := []struct {
		in, addr, want string
		ok             bool
	}{
		{"postgresql://u:p%40x@postgres.ns.svc:5432/db?sslmode=disable", "pgbouncer.ns.svc:6432", "postgresql://u:p%40x@pgbouncer.ns.svc:6432/db?sslmode=disable", true},
		{"host=a port=5432 user=u dbname=d", "b:6432", "host=a port=5432 user=u dbname=d host=b port=6432", true},
		{"postgresql://u@h:5432/db", "nonsense", "postgresql://u@h:5432/db", false},
	}
	for _, c := range cases {
		got, ok := dsnWithHostPort(c.in, c.addr)
		if got != c.want || ok != c.ok {
			t.Errorf("dsnWithHostPort(%q,%q) = %q,%v want %q,%v", c.in, c.addr, got, ok, c.want, c.ok)
		}
	}
}

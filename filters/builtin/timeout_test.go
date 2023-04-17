package builtin

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/zalando/skipper/eskip"
	"github.com/zalando/skipper/filters"
	"github.com/zalando/skipper/filters/filtertest"
	"github.com/zalando/skipper/net"
	"github.com/zalando/skipper/proxy/proxytest"
)

func TestBackendTimeout(t *testing.T) {
	bt := NewBackendTimeout()
	if bt.Name() != filters.BackendTimeoutName {
		t.Error("wrong name")
	}

	f, err := bt.CreateFilter([]interface{}{"2s"})
	if err != nil {
		t.Error("wrong id")
	}

	c := &filtertest.Context{FRequest: &http.Request{}, FStateBag: make(map[string]interface{})}
	f.Request(c)

	if c.FStateBag[filters.BackendTimeout] != 2*time.Second {
		t.Error("wrong timeout")
	}

	// second filter overwrites
	f, _ = bt.CreateFilter([]interface{}{"5s"})
	f.Request(c)

	if c.FStateBag[filters.BackendTimeout] != 5*time.Second {
		t.Error("overwrite expected")
	}
}

func TestFilterTimeouts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     string
		filter   *timeout
		workTime time.Duration
		want     int
		wantErr  bool
	}{
		{
			name:     "BackendTimeout bigger than backend time should return 200",
			args:     "1s",
			filter:   NewBackendTimeout().(*timeout),
			workTime: 100 * time.Millisecond,
			want:     http.StatusOK,
			wantErr:  false,
		}, {
			name:     "BackendTimeout smaller than backend time should timeout",
			args:     "10ms",
			filter:   NewBackendTimeout().(*timeout),
			workTime: 100 * time.Millisecond,
			want:     http.StatusGatewayTimeout,
			wantErr:  false,
		}, {
			name:     "ReadTimeout bigger than reading time should return 200",
			args:     "1s",
			filter:   NewReadTimeout().(*timeout),
			workTime: 10 * time.Millisecond,
			want:     http.StatusOK,
			wantErr:  false,
		}, {
			name:     "ReadTimeout smaller than reading time should timeout",
			args:     "15ms",
			filter:   NewReadTimeout().(*timeout),
			workTime: 5 * time.Millisecond,
			want:     499,
			wantErr:  false,
		}} {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch tt.filter.typ {
				case requestTimeout:
					time.Sleep(tt.workTime)
				}

				defer r.Body.Close()

				_, err := io.Copy(io.Discard, r.Body)
				if err != nil {
					t.Logf("body read timeout: %v", err)
					w.WriteHeader(499)
					w.Write([]byte("client timeout: " + err.Error()))
					return
				}

				ctx := r.Context()
				select {
				case <-ctx.Done():
					if err := ctx.Err(); err != nil {
						t.Logf("backend handler observes error form context: %v", err)
						w.WriteHeader(498) // ???
						w.Write([]byte("context error: " + err.Error()))
						return
					}
				default:
					t.Log("default")
				}

				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			}))
			defer backend.Close()

			fr := make(filters.Registry)
			fr.Register(tt.filter)
			r := &eskip.Route{Filters: []*eskip.Filter{{Name: tt.filter.Name(), Args: []interface{}{tt.args}}}, Backend: backend.URL}

			proxy := proxytest.New(fr, r)
			defer proxy.Close()
			reqURL, err := url.Parse(proxy.URL)
			//reqURL, err := url.Parse(backend.URL)
			if err != nil {
				t.Fatalf("Failed to parse url %s: %v", proxy.URL, err)
			}

			client := net.NewClient(net.Options{})
			defer client.Close()

			var req *http.Request
			switch tt.filter.typ {
			case requestTimeout:
				req, err = http.NewRequest("GET", reqURL.String(), nil)
				if err != nil {
					t.Fatal(err)
				}
			case readTimeout:
				dat := bytes.NewBufferString("abcdefghijklmn")
				req, err = http.NewRequest("POST", reqURL.String(), &slowReader{
					data: dat,
					d:    tt.workTime,
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			rsp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer rsp.Body.Close()

			if rsp.StatusCode != tt.want {
				t.Fatalf("Failed to get %d, got %d", tt.want, rsp.StatusCode)
			}
		})
	}

}

type slowReader struct {
	data *bytes.Buffer
	d    time.Duration
}

func (sr *slowReader) Read(b []byte) (int, error) {
	r := io.LimitReader(sr.data, 2)
	n, err := r.Read(b)
	time.Sleep(sr.d)
	return n, err
}

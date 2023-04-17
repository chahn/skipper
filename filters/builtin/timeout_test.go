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
		}, {
			name:     "WriteTimeout bigger than writing time should return 200",
			args:     "1s",
			filter:   NewWriteTimeout().(*timeout),
			workTime: 10 * time.Millisecond,
			want:     http.StatusOK,
			wantErr:  false,
		}, {
			name:     "WriteTimeout smaller than writing time should timeout",
			args:     "25ms",
			filter:   NewWriteTimeout().(*timeout),
			workTime: 5 * time.Millisecond,
			want:     200,
			wantErr:  true,
		}} {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch tt.filter.typ {
				case requestTimeout:
					time.Sleep(tt.workTime)
				case writeTimeout:
					r.Body.Close()
					_, err := io.Copy(io.Discard, r.Body)
					if err != nil {
						t.Logf("Failed to copy body: %v", err)
					}

					writer := newLimitWriter(w, 2, tt.workTime)
					w.WriteHeader(http.StatusOK)
					now := time.Now()
					n, err := writer.Write([]byte("abcdefghijklmnopq"))
					if err != nil {
						t.Logf("Failed to write: %v", err)
					}
					t.Logf("Wrote %d bytes, %s", n, time.Since(now))

					return
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
			if err != nil {
				t.Fatalf("Failed to parse url %s: %v", proxy.URL, err)
			}

			client := net.NewClient(net.Options{})
			defer client.Close()

			var req *http.Request
			switch tt.filter.typ {
			case writeTimeout:
				fallthrough
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

			// test write timeout
			if tt.wantErr && tt.filter.typ == writeTimeout {
				if rsp != nil {
					t.Fatal("rsp should be nil")
				}
				if err == nil {
					t.Fatalf("write timeout should cause a response error")
				}

				t.Logf("response error: %v", err)
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			defer rsp.Body.Close()

			if rsp.StatusCode != tt.want {
				t.Fatalf("Failed to get %d, got %d", tt.want, rsp.StatusCode)
			}
			_, err = io.Copy(io.Discard, rsp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)

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

// limitWriter is a modified copy from
// https://skia.googlesource.com/buildbot/+/a2c929bb01b6/go/util/limitwriter/limitwriter.go,
// which is licensed as BSD-3-Clause accoding to
// https://pkg.go.dev/go.skia.org/infra/go/util/limitwriter?tab=licenses
// with a change to accessibility.
//
// LimitWriter implements io.Writer and writes the data to an
// io.Writer, but limits the total bytes written to it, dropping the
// remaining bytes on the floor.
type limitWriter struct {
	dst   io.Writer
	limit int
	d     time.Duration
}

// New create a new LimitWriter that accepts at most 'limit' bytes.
func newLimitWriter(dst io.Writer, limit int, d time.Duration) *limitWriter {
	return &limitWriter{
		dst:   dst,
		limit: limit,
		d:     d,
	}
}
func (l *limitWriter) Write(p []byte) (int, error) {
	var err error
	lp := len(p)
	i := l.limit
	for n := lp; n > 0; n -= i {
		if l.limit > 0 {
			if lp > l.limit {
				p = p[:l.limit]
			}
			l.limit -= len(p)
			_, err = l.dst.Write(p)
		}
		time.Sleep(l.d)
	}
	return lp, err
}

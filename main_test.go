// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestReadIP(t *testing.T) {
	for _, tt := range []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{
			name:       "first entry of the proxy chain wins",
			headers:    map[string]string{"X-Forwarded-For": " 203.0.113.7 , 198.51.100.1 "},
			remoteAddr: "10.0.0.1:1234",
			want:       "203.0.113.7",
		},
		{
			name:       "real ip is the fallback",
			headers:    map[string]string{"X-Forwarded-For": " , 198.51.100.1", "X-Real-Ip": " 203.0.113.8 "},
			remoteAddr: "10.0.0.1:1234",
			want:       "203.0.113.8",
		},
		{
			name:       "app engine header",
			headers:    map[string]string{"X-Appengine-Remote-Addr": "203.0.113.9"},
			remoteAddr: "10.0.0.1:1234",
			want:       "203.0.113.9",
		},
		{
			name:       "remote address when no proxy set a header",
			remoteAddr: "203.0.113.10:4321",
			want:       "203.0.113.10",
		},
		{
			name:       "ipv6 remote address",
			remoteAddr: "[2001:db8::1]:4321",
			want:       "2001:db8::1",
		},
		{
			name:       "unparsable remote address",
			remoteAddr: "not-an-address",
			want:       "unknown",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			if got := readIP(r); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLogging(t *testing.T) {
	var out bytes.Buffer
	served := false
	h := logging(log.New(&out, "", 0))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusTeapot)
	}))

	r := httptest.NewRequest(http.MethodGet, "/books/", nil)
	r.RemoteAddr = "203.0.113.11:4321"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if !served {
		t.Fatal("the middleware did not call the next handler")
	}
	if w.Code != http.StatusTeapot {
		t.Errorf("got status %d, want %d", w.Code, http.StatusTeapot)
	}
	if got, want := strings.TrimSpace(out.String()), "203.0.113.11 GET /books/"; got != want {
		t.Errorf("got log line %q, want %q", got, want)
	}
}

// The site must be embedded and reachable through the same handler main wires up.
func TestEmbeddedSite(t *testing.T) {
	r := http.NewServeMux()
	r.Handle("/", FileServer(fsys))

	for _, target := range []string{"/", "/index.html", "/401.html", "/404.html", "/login-sdk.js"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code >= http.StatusInternalServerError {
			t.Errorf("GET %s: got status %d", target, w.Code)
		}
	}
}

// The site must answer real requests over TCP and stop cleanly when its
// context is cancelled.
func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	url := "http://" + ln.Addr().String()

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, log.New(&out, "", 0)) }()

	resp, err := http.Get(url + "/401.html")
	if err != nil {
		t.Fatalf("cannot reach the site: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if !bytes.Equal(body, mustRead(t, "401.html")) {
		t.Error("the served body is not the embedded 401 page")
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve reported %v, want a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after its context was cancelled")
	}

	if _, err := http.Get(url + "/"); err == nil {
		t.Error("the site still answers after shutdown")
	}
	if got := out.String(); !strings.Contains(got, "shutting down") {
		t.Errorf("the shutdown was not logged, got %q", got)
	}
}

// Docker and Kubernetes stop a container with SIGTERM. Listening for SIGINT
// alone let a container stop skip the graceful shutdown path.
func TestNotifyShutdown(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := notifyShutdown(context.Background())
			defer stop()

			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatalf("cannot send %v: %v", sig, err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				t.Fatalf("%v did not cancel the shutdown context", sig)
			}
		})
	}
}

func TestRun(t *testing.T) {
	t.Run("serves until cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ran := make(chan error, 1)
		go func() { ran <- run(ctx, "127.0.0.1:0", log.New(io.Discard, "", 0)) }()

		cancel()
		select {
		case err := <-ran:
			if err != nil {
				t.Fatalf("run reported %v, want a clean shutdown", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("run did not return after its context was cancelled")
		}
	})

	t.Run("reports an unusable address", func(t *testing.T) {
		err := run(context.Background(), "127.0.0.1:not-a-port", log.New(io.Discard, "", 0))
		if err == nil {
			t.Fatal("run accepted an unusable address")
		}
	})
}

// The shipped binary must read MAIN_ADDR, serve the embedded site, and exit
// cleanly on the SIGTERM that a container stop sends.
func TestBinaryEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("the end to end test builds the binary")
	}

	bin := filepath.Join(t.TempDir(), "main")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the site: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var out lockedBuffer
	site := exec.Command(bin)
	site.Env = append(os.Environ(), "MAIN_ADDR="+addr)
	site.Stdout, site.Stderr = &out, &out
	if err := site.Start(); err != nil {
		t.Fatalf("cannot start the site: %v", err)
	}
	defer site.Process.Kill()

	var resp *http.Response
	for range 100 {
		if resp, err = http.Get("http://" + addr + "/"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the site never came up: %v\n%s", err, out.String())
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !bytes.Equal(body, mustRead(t, "index.html")) {
		t.Error("the served body is not the embedded home page")
	}

	if err := site.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("cannot send SIGTERM: %v", err)
	}
	if err := site.Wait(); err != nil {
		t.Fatalf("the site exited with %v\n%s", err, out.String())
	}
	if got := out.String(); !strings.Contains(got, "goodbye!") {
		t.Errorf("the site did not shut down gracefully, got %q", got)
	}
}

// lockedBuffer collects the subprocess output that Wait and the test read
// from different goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

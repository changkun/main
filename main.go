// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func init() {
	log.SetPrefix("main")
}

var (
	//go:embed static/*
	static embed.FS
	fsys   fs.FS
)

func init() {
	subFS, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	fsys = subFS
}

func main() {
	l := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile|log.Lmsgprefix)

	ctx, stop := notifyShutdown(context.Background())
	defer stop()

	if err := run(ctx, os.Getenv("MAIN_ADDR"), l); err != nil {
		l.Fatalf("changkun.de stopped: %v", err)
	}
	l.Println("goodbye!")
}

// run serves the site on addr until ctx is cancelled. An empty addr means
// port 80 on every interface, which is what the container exposes.
func run(ctx context.Context, addr string, l *log.Logger) error {
	if addr == "" {
		addr = "0.0.0.0:80"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serve(ctx, ln, l)
}

// notifyShutdown returns a context that is cancelled when the site is asked to
// stop. Container runtimes send SIGTERM, a terminal sends SIGINT; both must
// reach the graceful shutdown path. Once the first signal arrives the handlers
// are removed again, so a second one terminates the process immediately.
func notifyShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// serve answers requests on ln until ctx is cancelled, then drains the
// in-flight ones. A clean shutdown reports no error.
func serve(ctx context.Context, ln net.Listener, l *log.Logger) error {
	r := http.NewServeMux()
	r.Handle("/", FileServer(fsys))
	s := &http.Server{
		Handler:           logging(l)(r),
		ErrorLog:          l,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      time.Minute,
		IdleTimeout:       time.Minute,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		l.Println("changkun.de is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		s.SetKeepAlivesEnabled(false)
		if err := s.Shutdown(ctx); err != nil {
			l.Printf("cannot gracefully shutdown changkun.de: %v", err)
		}
	}()

	l.Printf("changkun.de is serving on %s...", ln.Addr())
	err := s.Serve(ln)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func logging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				logger.Println(readIP(r), r.Method, r.URL.Path)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func readIP(r *http.Request) string {
	forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	if clientIP := strings.TrimSpace(forwarded); clientIP != "" {
		return clientIP
	}
	if clientIP := strings.TrimSpace(r.Header.Get("X-Real-Ip")); clientIP != "" {
		return clientIP
	}
	if addr := r.Header.Get("X-Appengine-Remote-Addr"); addr != "" {
		return addr
	}
	ip, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return "unknown" // use unknown to guarantee non empty string
	}
	return ip
}

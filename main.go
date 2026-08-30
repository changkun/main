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
	fsys   http.FileSystem
)

func init() {
	subFS, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	fsys = http.FS(subFS)
}

func main() {
	l := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile|log.Lmsgprefix)

	addr := os.Getenv("MAIN_ADDR")
	if addr == "" {
		addr = "0.0.0.0:80"
	}

	r := http.NewServeMux()
	r.Handle("/", FileServer(fsys))
	s := &http.Server{
		Addr:              addr,
		Handler:           logging(l)(r),
		ErrorLog:          l,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      time.Minute,
		IdleTimeout:       time.Minute,
	}

	// Container runtimes stop a service with SIGTERM; Ctrl-C sends SIGINT.
	// Both must reach the graceful shutdown path.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		stop() // a second signal now terminates immediately
		l.Println("changkun.de is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		s.SetKeepAlivesEnabled(false)
		if err := s.Shutdown(ctx); err != nil {
			l.Printf("cannot gracefully shutdown changkun.de: %v", err)
		}
	}()

	l.Printf("changkun.de is serving on %s...", addr)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		l.Fatalf("cannot listen on %s, err: %v", addr, err)
	}

	<-done
	l.Println("goodbye!")
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

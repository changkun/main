// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"time"
)

//go:embed static/*
var static embed.FS

// changkunFS implements fs.FS
type changkunFS struct {
	content embed.FS
}

func (c changkunFS) Open(name string) (fs.File, error) {
	return c.content.Open(path.Join("static", name))
}

func main() {
	l := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile|log.Lmsgprefix)
	logger := logging(l)

	r := http.NewServeMux()
	r.Handle("/", http.FileServer(http.FS(changkunFS{static})))

	const addr = "0.0.0.0:9121"
	s := &http.Server{
		Addr:         addr,
		Handler:      logger(r),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: time.Minute,
		IdleTimeout:  time.Minute,
	}

	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		l.Println("changkun.de is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		s.SetKeepAlivesEnabled(false)
		if err := s.Shutdown(ctx); err != nil {
			l.Fatalf("cannot gracefully shutdown changkun.de: %v", err)
		}
		close(done)
	}()

	l.Printf("changkun.de is serving on %s...", addr)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		l.Fatalf("cannot listen on %s, err: %v\n", addr, err)
	}

	l.Println("goodbye!")
	<-done
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
	clientIP := r.Header.Get("X-Forwarded-For")
	clientIP = strings.TrimSpace(strings.Split(clientIP, ",")[0])
	if clientIP == "" {
		clientIP = strings.TrimSpace(r.Header.Get("X-Real-Ip"))
	}
	if clientIP != "" {
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

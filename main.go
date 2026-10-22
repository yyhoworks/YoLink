package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const appVersion = "0.14.2"

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// listenPort extracts the port from a STRMLINK_LISTEN address so the
// healthcheck probes the same port the server actually binds; it falls back
// to the default 8796 when the address carries no usable port.
func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "8796"
	}
	return port
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		r, e := client.Get("http://127.0.0.1:" + listenPort(env("STRMLINK_LISTEN", ":8796")) + "/healthz")
		if e != nil {
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	data := env("STRMLINK_DATA", "./data")
	output := env("STRMLINK_OUTPUT", "./strm-test")
	if len(os.Args) == 1 || os.Args[1] != "restore-rollback" {
		if err := checkStartupPermissions(data, output); err != nil {
			log.Fatal(err)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "check-permissions" {
		log.Print("数据和输出目录权限检查通过")
		return
	}
	dataLock, e := acquireDataLock(data)
	if e != nil {
		log.Fatal(e)
	}
	defer dataLock.Close()
	restore, e := openRestoreManager(data)
	if e != nil {
		log.Fatal(e)
	}
	defer restore.Close()
	if len(os.Args) > 1 && os.Args[1] == "restore-rollback" {
		if e = runRestoreRollbackCLI(ctx, restore); e != nil {
			log.Fatal(e)
		}
		log.Print("恢复回退完成；请正常启动服务并核对恢复资料")
		return
	}
	recovery, e := restore.recoverStartup(ctx)
	if e != nil {
		log.Fatal("恢复保护未完成，拒绝启动: ", e)
	}
	s, e := openStoreForStartup(data, recovery)
	if e != nil {
		log.Fatal(e)
	}
	defer s.db.Close()
	if base := os.Getenv("STRMLINK_PUBLIC_BASE"); base != "" {
		v, e := validBase(base)
		if e != nil {
			log.Fatal(e)
		}
		if e = s.setSetting("public_base", v); e != nil {
			log.Fatal(e)
		}
	}
	providers := newProviders(s)
	engine, e := newEngine(ctx, s, providers, output)
	if e != nil {
		log.Fatal(e)
	}
	defer engine.Close()
	server, e := newServer(s, engine, providers, data, output)
	if e != nil {
		log.Fatal(e)
	}
	server.restart = stop
	addr := env("STRMLINK_LISTEN", ":8796")
	httpServer := &http.Server{Addr: addr, Handler: server.handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
		close(shutdownDone)
	}()
	log.Printf("YoLink %s listening on %s; first-login password file: %s/initial-admin-password.txt (changed passwords persist in admin-credential.json)", appVersion, addr, data)
	if e = httpServer.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Fatal(e)
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
}

package main

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"
)

func waitGateQueue(t *testing.T, g *accountGate, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		count := len(g.waiters)
		g.mu.Unlock()
		if count == n {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("account gate did not queue %d waiters", n)
}

func TestAccountGatePrioritizesPlaybackWithoutStarvingScan(t *testing.T) {
	g := &accountGate{}
	g.Lock()
	order := make(chan int, 7)
	start := func(id int, high bool) {
		go func() {
			if err := g.acquire(context.Background(), high); err != nil {
				return
			}
			order <- id
			g.Unlock()
		}()
	}
	start(0, false)
	waitGateQueue(t, g, 1)
	for i := 1; i <= 6; i++ {
		start(i, true)
		waitGateQueue(t, g, i+1)
	}
	g.Unlock()
	for _, want := range []int{1, 2, 3, 4, 0, 5, 6} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("grant order %d; want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("gate stalled")
		}
	}
}

func TestAccountGateCancellationAndGrantRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		g := &accountGate{}
		g.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			err := g.acquire(ctx, true)
			if err == nil {
				g.Unlock()
			}
			done <- err
		}()
		waitGateQueue(t, g, 1)
		go cancel()
		g.Unlock()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel/grant race stalled")
		}
		check, stop := context.WithTimeout(context.Background(), time.Second)
		if err := g.acquire(check, false); err != nil {
			stop()
			t.Fatal("lease leaked", err)
		}
		g.Unlock()
		stop()
	}
}

func TestAccountGateQueuedCancellationAndIndependentAccounts(t *testing.T) {
	p := newProviders(nil)
	g := p.lock("a")
	g.Lock()
	defer g.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.acquire(ctx, false) }()
	waitGateQueue(t, g, 1)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal("cancellation not propagated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter remains blocked")
	}
	waitGateQueue(t, g, 0)
	other, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := p.lock("b").acquire(other, true); err != nil {
		t.Fatal("unrelated account blocked", err)
	}
	p.lock("b").Unlock()
}

func TestPlaybackRunsBetweenDirectoryPages(t *testing.T) {
	f := setup(t)
	a := Account{ID: "ali-priority", Provider: "ali", Root: "root", Drive: "drive", Secrets: map[string]string{"access_token": "fixture-token"}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	first, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	pages := 0
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/adrive/v1.0/openFile/getDownloadUrl" {
			mu.Lock()
			order = append(order, "playback")
			mu.Unlock()
			return response115(r, object{"url": "https://cdn.example.test/movie"}), nil
		}
		pages++
		if pages == 1 {
			mu.Lock()
			order = append(order, "page1")
			mu.Unlock()
			close(first)
			<-release
			return response115(r, object{"items": []object{{"file_id": "1", "name": "one.mkv"}}, "next_marker": "next"}), nil
		}
		mu.Lock()
		order = append(order, "page2")
		mu.Unlock()
		return response115(r, object{"items": []object{{"file_id": "2", "name": "two.mkv"}}, "next_marker": ""}), nil
	})
	listed := make(chan error, 1)
	go func() {
		files, err := p.List(context.Background(), a.ID, a.Root)
		if err == nil && len(files) != 2 {
			err = errors.New("incomplete listing")
		}
		listed <- err
	}()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first page not reached")
	}
	resolved := make(chan error, 1)
	go func() {
		_, err := p.Resolve(context.Background(), Media{Account: a.ID, RemoteID: "1"})
		resolved <- err
	}()
	waitGateQueue(t, p.lock(a.ID), 1)
	close(release)
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if err := <-listed; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "page1" || order[1] != "playback" || order[2] != "page2" {
		t.Fatal("playback delayed by directory", order)
	}
}

func TestAccountMutationInvalidatesInFlightListing(t *testing.T) {
	f := setup(t)
	a := Account{ID: "ali-edit", Provider: "ali", Root: "root", Drive: "drive", Revision: 1, Secrets: map[string]string{"access_token": "old-token"}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	first, release := make(chan struct{}), make(chan struct{})
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(first)
		<-release
		return response115(r, object{"items": []object{{"file_id": "1", "name": "one.mkv"}}, "next_marker": "next"}), nil
	})
	listed := make(chan error, 1)
	go func() {
		files, err := p.List(context.Background(), a.ID, a.Root)
		if err != nil && len(files) != 0 {
			err = errors.New("partial listing escaped")
		}
		listed <- err
	}()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first page not reached")
	}
	mutated := make(chan error, 1)
	go func() {
		g := p.lock(a.ID)
		g.Lock()
		defer g.Unlock()
		a.Revision++
		a.Secrets["access_token"] = "new-token"
		mutated <- f.s.saveAccount(a)
	}()
	waitGateQueue(t, p.lock(a.ID), 1)
	close(release)
	if err := <-mutated; err != nil {
		t.Fatal(err)
	}
	if err := <-listed; classifyError(err) != "configuration" {
		t.Fatal("account revision change ignored", err)
	}
	saved, err := f.s.account(a.ID)
	if err != nil || saved.Secrets["access_token"] != "new-token" {
		t.Fatal("new credentials overwritten")
	}
}

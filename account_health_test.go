package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthObservationsSeparateOperationsAndDeduplicateTransitions(t *testing.T) {
	f := setup(t)
	now := time.Now()
	observe := func(op string, after time.Duration, err error) {
		t.Helper()
		if er := f.s.observeAccount(f.a, op, now.Add(after), now.Add(after+time.Millisecond), err); er != nil {
			t.Fatal(er)
		}
	}
	health, err := f.s.accountHealth(f.a)
	if err != nil || len(health) != 3 || health[0].State != "unknown" {
		t.Fatal(health, err)
	}
	limited := cloudHTTPError(429, "120", now)
	observe("list", 0, limited)
	if _, err = f.s.db.Exec("UPDATE notifications SET read=1"); err != nil {
		t.Fatal(err)
	}
	observe("list", time.Second, limited)
	observe("quota", 2*time.Second, nil)
	health, err = f.s.accountHealth(f.a)
	if err != nil || health[0].State != "rate_limit" || health[0].Failures != 2 || !health[0].Retryable || health[0].NextRetry < now.Add(121*time.Second).UnixMilli() || health[2].State != "healthy" {
		t.Fatal("operation state lost", health, err)
	}
	var notices, unread int
	if err = f.s.db.QueryRow("SELECT count(*),COALESCE(sum(1-read),0) FROM notifications").Scan(&notices, &unread); err != nil || notices != 1 || unread != 0 {
		t.Fatal("repeat failure spammed notification", notices, unread, err)
	}
	observe("list", 3*time.Second, nil)
	health, err = f.s.accountHealth(f.a)
	if err != nil || health[0].State != "healthy" || health[0].Failures != 0 || health[0].LastSuccess == 0 || health[0].NextRetry != 0 {
		t.Fatal("success did not recover state", health, err)
	}
	if err = f.s.db.QueryRow("SELECT count(*),COALESCE(sum(1-read),0) FROM notifications").Scan(&notices, &unread); err != nil || notices != 1 || unread != 1 {
		t.Fatal("recovery transition missing", notices, unread, err)
	}
	observe("list", 4*time.Second, context.Canceled)
	observe("list", time.Second, cloudHTTPError(401, "", now))
	health, err = f.s.accountHealth(f.a)
	if err != nil || health[0].State != "healthy" {
		t.Fatal("cancelled or old request overwrote state", health, err)
	}
}

func TestHealthRejectsStaleRevisionAndNeverPersistsRawErrors(t *testing.T) {
	f := setup(t)
	now := time.Now()
	secret := "secret-fixture-do-not-export"
	if err := f.s.observeAccount(f.a, "resolve", now, now, errors.New("cookie="+secret+" https://fixture.test/?k="+secret)); err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.db.Query("SELECT payload FROM account_health UNION ALL SELECT body FROM notifications")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if strings.Contains(raw, secret) {
			rows.Close()
			t.Fatal("raw error persisted")
		}
	}
	rows.Close()
	a := f.a
	a.Revision++
	if err = f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	if err = f.s.observeAccount(f.a, "list", now, now, nil); err != nil {
		t.Fatal(err)
	}
	health, err := f.s.accountHealth(a)
	if err != nil || health[0].State != "unknown" || health[1].State != "unknown" {
		t.Fatal("stale health visible", health, err)
	}
	a.Disabled = true
	if err = f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	if err = f.s.observeAccount(a, "quota", now, now, nil); err != nil {
		t.Fatal(err)
	}
	health, err = f.s.accountHealth(a)
	if err != nil || health[2].State != "disabled" {
		t.Fatal("disabled state lost", health, err)
	}
}

func TestProviderHealthTracksWholeListingFailureNotEarlierSuccessfulPage(t *testing.T) {
	f := setup(t)
	a := Account{ID: "health-ali", Name: "fixture", Provider: "ali", Drive: "d", Root: "root", Secrets: map[string]string{"access_token": "fixture"}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	calls := 0
	p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response115(r, object{"items": []object{{"file_id": "1", "name": "movie.mkv"}}, "next_marker": "next"}), nil
		}
		resp := response115(r, object{})
		resp.StatusCode = 429
		resp.Header.Set("Retry-After", "60")
		return resp, nil
	})
	if _, err := p.List(context.Background(), a.ID, a.Root); classifyError(err) != "rate_limit" {
		t.Fatal(err)
	}
	health, err := f.s.accountHealth(a)
	if err != nil || health[0].State != "rate_limit" || health[0].LastSuccess != 0 || health[0].Failures != 1 {
		t.Fatal("partial listing marked healthy", health, err)
	}
}

func healthFailedJob(t *testing.T, f *fixture, id string) Job {
	t.Helper()
	j := Job{ID: id, Rule: f.r.ID, Snapshot: &f.r, Folder: f.r.Folder, SourcePath: f.r.SourcePath, Status: "failed", ErrorKind: "credentials", Created: time.Now().Unix()}
	if err := f.s.saveJob(j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestHealthRecoveryRequiresFreshCheckRevisionConfirmationAndOwnership(t *testing.T) {
	f := setup(t)
	f.e.Close() // queued jobs stay inspectable and cannot contact anything
	j := healthFailedJob(t, f, "recover-this")
	h, cookie := generationHandler(t, f)
	endpoint := "/api/accounts/" + f.a.ID + "/recover"
	body := object{"revision": f.a.Revision, "jobs": []string{j.ID}, "confirm": true}
	if w := generationPOST(h, cookie, endpoint, body); w.Code != 409 {
		t.Fatal("unchecked account recovered", w.Code)
	}
	now := time.Now()
	if err := f.s.observeAccount(f.a, "list", now.Add(-10*time.Minute), now.Add(-10*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if w := generationPOST(h, cookie, endpoint, body); w.Code != 409 {
		t.Fatal("old health accepted", w.Code)
	}
	if err := f.s.observeAccount(f.a, "list", now, now, nil); err != nil {
		t.Fatal(err)
	}
	body["confirm"] = false
	if w := generationPOST(h, cookie, endpoint, body); w.Code != 400 {
		t.Fatal("missing confirmation accepted", w.Code)
	}
	body["confirm"], body["revision"] = true, 999
	if w := generationPOST(h, cookie, endpoint, body); w.Code != 409 {
		t.Fatal("wrong revision accepted", w.Code)
	}
	body["revision"], body["jobs"] = f.a.Revision, []string{j.ID, j.ID, "not-mine"}
	w := generationPOST(h, cookie, endpoint, body)
	var response struct {
		Started  []Job    `json:"started"`
		Deferred []object `json:"deferred"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Started) != 1 || len(response.Deferred) != 1 {
		t.Fatal("recovery result incorrect", w.Code, w.Body.String())
	}
	if response.Started[0].RetryOf != j.ID || response.Started[0].SourcePath != j.SourcePath {
		t.Fatal("recovery scope changed")
	}
	w = generationPOST(h, cookie, endpoint, body)
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Started) != 0 {
		t.Fatal("duplicate recovery queued")
	}
	req := httptest.NewRequest("GET", "/api/accounts/"+f.a.ID+"/health", nil)
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, req)
	if unauthorized.Code != 401 {
		t.Fatal("health exposed without authentication", unauthorized.Code)
	}
}

func TestHealthRecoveryRejectsChangedRuleOptionsAndRateLimit(t *testing.T) {
	f := setup(t)
	f.e.Close()
	j := healthFailedJob(t, f, "old-rule")
	h, cookie := generationHandler(t, f)
	now := time.Now()
	if err := f.s.observeAccount(f.a, "list", now, now, nil); err != nil {
		t.Fatal(err)
	}
	r := f.r
	r.CopySubtitles = !r.CopySubtitles
	if err := f.s.saveRule(r); err != nil {
		t.Fatal(err)
	}
	body := object{"revision": 0, "jobs": []string{j.ID}, "confirm": true}
	w := generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/recover", body)
	var result struct {
		Started  []Job    `json:"started"`
		Deferred []object `json:"deferred"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Started) != 0 || len(result.Deferred) != 1 {
		t.Fatal("changed copy settings restored", w.Code, w.Body.String())
	}
	if err := f.s.observeAccount(f.a, "resolve", now, now, cloudHTTPError(429, "", now)); err != nil {
		t.Fatal(err)
	}
	if w = generationPOST(h, cookie, "/api/accounts/"+f.a.ID+"/recover", body); w.Code != 409 {
		t.Fatal("limited account recovered", w.Code)
	}
}

func TestHealthAffectedJobsFilteredBeforePaginationIncludesLegacy(t *testing.T) {
	f := setup(t)
	f.e.Close()
	healthFailedJob(t, f, "old-selected")
	legacy := Job{ID: "legacy", Rule: f.r.ID, Status: "failed", Created: 1}
	if err := f.s.saveJob(legacy); err != nil {
		t.Fatal(err)
	}
	other := f.r
	other.ID = "other-rule"
	other.Account = "other-account"
	tx, err := f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		if err = saveJobTx(tx, Job{ID: fmt.Sprint("other-", i), Rule: other.ID, Snapshot: &other, Status: "failed", Created: 2}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	jobs, next, err := f.s.accountRecoveryJobs(f.a.ID, 0)
	if err != nil || len(jobs) != 2 || next != 0 {
		t.Fatal("other accounts hid failures", len(jobs), next, err)
	}
	h, cookie := generationHandler(t, f)
	req := httptest.NewRequest("GET", "/api/accounts/"+f.a.ID+"/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "缺少规则快照") {
		t.Fatal("legacy failure hidden", w.Code, w.Body.String())
	}
	tx, err = f.s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if err = saveJobTx(tx, Job{ID: fmt.Sprint("selected-", i), Rule: f.r.ID, Snapshot: &f.r, Status: "failed"}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	jobs, next, err = f.s.accountRecoveryJobs(f.a.ID, 0)
	if err != nil || len(jobs) != 100 || next == 0 {
		t.Fatal("missing next page", len(jobs), next, err)
	}
	last, lastNext, err := f.s.accountRecoveryJobs(f.a.ID, next)
	if err != nil || len(last) != 3 || lastNext != 0 {
		t.Fatal("pagination lost failures", len(last), lastNext, err)
	}
}

func TestQuotaUnsupportedDoesNotRaiseFailureNotification(t *testing.T) {
	f := setup(t)
	a := Account{ID: "unsupported-quota", Name: "Unsupported", Provider: "unsupported", Root: "0", Secrets: map[string]string{"access_token": "fixture", "device_id": "fixture"}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported quota contacted network")
		return nil, nil
	})
	if _, err := p.Quota(context.Background(), a.ID); classifyError(err) != "unsupported" {
		t.Fatal(err)
	}
	health, err := f.s.accountHealth(a)
	if err != nil || health[2].State != "unsupported" || health[2].Failures != 0 {
		t.Fatal(health, err)
	}
	var count int
	if err = f.s.db.QueryRow("SELECT count(*) FROM notifications").Scan(&count); err != nil || count != 0 {
		t.Fatal("unsupported capability raised warning", count, err)
	}
	a.Secrets = map[string]string{}
	if err = f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Quota(context.Background(), a.ID); classifyError(err) != "unsupported" {
		t.Fatal("unsupported query attempted authorization", err)
	}
}

func Test139RefreshRetainsTransientAndMalformedErrorTypes(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		status     int
		body, kind string
	}{{429, "ignored", "rate_limit"}, {503, "ignored", "network"}, {200, "not xml", "protocol"}, {200, "<root><return>99</return></root>", "other"}} {
		p := newProviders(f.s)
		p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		a := f.a
		err := p.refresh139(context.Background(), &a, []string{"fixture", "123", "old"})
		if classifyError(err) != tc.kind {
			t.Fatal("renewal misclassified", tc.status, err)
		}
		if tc.status == 429 {
			var cloud *CloudError
			if !errors.As(err, &cloud) || cloud.RetryAfter != 120*time.Second {
				t.Fatal("renewal Retry-After lost")
			}
		}
	}
}

func TestCredentialPersistenceFailureIsStorageNotCredentialFailure(t *testing.T) {
	f := setup(t)
	a := Account{ID: "save-failure", Name: "fixture", Provider: "ali", Drive: "drive", Secrets: map[string]string{"access_token": "fixture"}}
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.Exec(`CREATE TRIGGER fail_account_save BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'isolated storage failure'); END;`); err != nil {
		t.Fatal(err)
	}
	p := newProviders(f.s)
	err := p.with(context.Background(), a.ID, func(a *Account) error { a.Secrets["access_token"] = "refreshed"; return nil })
	if classifyError(err) != "storage" {
		t.Fatal("storage reported as credential failure", err)
	}
}

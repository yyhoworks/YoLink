package main

import (
	"errors"
	"testing"
)

func TestAccountReportsRejectStaleCredentialRevision(t *testing.T) {
	f := setup(t)
	if _, err := f.s.updateAccountReport(f.a.ID, func(v *AccountReport) { v.Checked = 1; v.Reachable = true }, 0); err != nil {
		t.Fatal(err)
	}
	a := f.a
	a.Revision = 1
	if err := f.s.saveAccount(a); err != nil {
		t.Fatal(err)
	}
	reports, err := f.s.accountReports()
	if err != nil || len(reports) != 0 {
		t.Fatal("old credential health still visible", reports, err)
	}
	called := false
	_, err = f.s.updateAccountReport(a.ID, func(v *AccountReport) { called = true; v.Checked = 2 }, 0)
	if !errors.Is(err, errAccountReportStale) || called {
		t.Fatal("stale check overwrote new revision", err)
	}
	updated, err := f.s.updateAccountReport(a.ID, func(v *AccountReport) { v.Quota = &QuotaUsage{Total: 100, Checked: 3} }, 1)
	if err != nil || updated.Revision != 1 || updated.Checked != 0 || updated.Reachable || updated.Quota == nil {
		t.Fatal("new quota inherited stale connection health", updated, err)
	}
}

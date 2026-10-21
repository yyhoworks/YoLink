package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInplaceProgressRecoveryContinuesAndRollsBack(t *testing.T) {
	for _, tail := range []string{"incomplete record", "missing newline"} {
		t.Run(tail, func(t *testing.T) {
			f := setup(t)
			s, h, c := testToolsServer(t, f, f.c)
			dir := filepath.Join(f.output, "migration")
			original := oldMigrationURL("yun139-1", "/电影 & 测试.mkv")
			first := migrationFile(t, dir, "one.strm", original)
			second := migrationFile(t, dir, "two.strm", original)
			p := makeInplacePlan(t, f, s, h, c, dir, nil)
			apply := "/api/maintenance/" + p.ID + "/apply"
			p = migrationPlanResponse(t, s, generationPOST(h, c, apply, object{"confirm_directory": p.Directory, "limit": 1}))
			if planSummary(p)["applied"] != 1 {
				t.Fatal("first batch was not checkpointed")
			}
			name := filepath.Join(f.data, "plans", p.ID+".progress")
			raw, err := os.ReadFile(name)
			mustSucceed(t, err)
			if tail == "incomplete record" {
				raw = append(raw, []byte(`{"index":1,"appl`)...)
			} else {
				raw = raw[:len(raw)-1]
			}
			mustSucceed(t, os.WriteFile(name, raw, 0600))
			p = migrationPlanResponse(t, s, generationPOST(h, c, apply, object{"confirm_directory": p.Directory, "limit": 1}))
			if planSummary(p)["applied"] != 2 {
				t.Fatal("recovered plan did not finish", planSummary(p))
			}
			// Reload and apply again: the new record must still be readable.
			p = migrationPlanResponse(t, s, generationPOST(h, c, apply, object{"confirm_directory": p.Directory}))
			if planSummary(p)["applied"] != 2 {
				t.Fatal("resumed progress did not survive reload")
			}
			p = migrationPlanResponse(t, s, generationPOST(h, c, "/api/maintenance/"+p.ID+"/rollback", object{}))
			if planSummary(p)["applied"] != 0 {
				t.Fatal("recovered plan did not roll back")
			}
			for _, name := range []string{first, second} {
				got, err := os.ReadFile(name)
				if err != nil || string(got) != string(original) {
					t.Fatal("rollback lost original STRM", err)
				}
			}
		})
	}
}

func TestInplaceProgressCorruptionPreservesJournal(t *testing.T) {
	for _, raw := range []string{
		"{\"index\":0,\"applied\":true}\nbroken\n{\"index\":0,\"applied\":false}",
		"{\"index\":0,\"applied\":true}\nbroken\n",
		"{\"index\":5,\"applied\":true}",
	} {
		dir := t.TempDir()
		mustSucceed(t, os.Mkdir(filepath.Join(dir, "plans"), 0700))
		name := filepath.Join(dir, "plans", "fixture.progress")
		mustSucceed(t, os.WriteFile(name, []byte(raw), 0600))
		s := &Server{s: &Store{dir: dir}}
		p := MaintenancePlan{ID: "fixture", Items: []PlanItem{{Content: "next"}}}
		if err := s.loadInplaceProgress(&p); err == nil {
			t.Fatal("corrupt complete record was accepted")
		}
		got, err := os.ReadFile(name)
		if err != nil || string(got) != raw {
			t.Fatal("corrupt journal was modified", err)
		}
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRestoreReserveAccountsForLargeNewInventoryAndTinyOldSet(t *testing.T) {
	p := restoreJournal{Format: 1, Schema: currentSchemaVersion, Runtime: restoreRuntime{strings.Repeat("a", 32), "installing", strings.Repeat("b", 32)}, Created: 1}
	for i := 0; i < 70000; i++ {
		p.New.Files = append(p.New.Files, backupFile{fmt.Sprintf("plans/%032x.json", i), 0, strings.Repeat("f", 64)})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 8<<20 {
		t.Fatal("fixture journal did not exceed fixed reserve")
	}
	budget, err := restoreReserveBytes(&p)
	if err != nil {
		t.Fatal(err)
	}
	if budget < int64(len(raw))*2+(8<<20) {
		t.Fatal("reserve omits transition journal bytes", budget, len(raw))
	}
}

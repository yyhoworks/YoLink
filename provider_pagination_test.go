package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
)

func TestProvidersRejectIncompleteDirectoryListings(t *testing.T) {
	for _, provider := range []string{"ali", "139", "139old", "quark", "quarktv"} {
		for _, scenario := range []string{"missing_list", "wrong_list", "missing_paging", "bad_paging", "empty_page", "duplicate_page", "changing_paging"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				f := setup(t)
				a := Account{ID: "pagination", Provider: provider, Root: "root", Drive: "drive", Secrets: map[string]string{
					"access_token": "fixture", "device_id": "fixture", "cookie": "fixture=value",
					"authorization": base64.StdEncoding.EncodeToString([]byte("fixture:123:token")), "api_host": "https://fixture.139.com",
				}}
				if err := f.s.saveAccount(a); err != nil {
					t.Fatal(err)
				}
				p := newProviders(f.s)
				calls := 0
				p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls > 3 {
						t.Fatal("pagination did not stop")
					}
					rows := []any{}
					for i := 0; i < 100; i++ {
						id := fmt.Sprint(i)
						rows = append(rows, object{"file_id": id, "fileId": id, "contentID": id, "fid": id, "name": id + ".mkv", "contentName": id + ".mkv", "file_name": id + ".mkv", "filename": id + ".mkv", "file_type": 1})
					}
					var list any = rows
					var total any = 150
					var marker any = "next"
					switch scenario {
					case "missing_list":
						list = nil
					case "wrong_list":
						list = object{}
					case "missing_paging":
						total, marker = nil, nil
					case "bad_paging":
						total, marker = "invalid", 42
					case "empty_page":
						if calls == 2 {
							list = []any{}
						}
					case "duplicate_page": // Second page repeats the first, including IDs.
					case "changing_paging":
						if calls == 2 {
							total = 99
							marker = "next"
						}
					}
					var result object
					switch provider {
					case "ali":
						result = object{"items": list, "next_marker": marker}
					case "139":
						result = object{"success": true, "data": object{"items": list, "nextPageCursor": marker}}
						if scenario == "missing_paging" {
							delete(obj(result["data"]), "nextPageCursor")
						}
					case "139old":
						result = object{"success": true, "data": object{"getDiskResult": object{"catalogList": []any{}, "contentList": list, "nodeCount": total}}}
					case "quark":
						result = object{"data": object{"list": list}, "metadata": object{"_total": total}}
					case "quarktv":
						result = object{"data": object{"files": list, "total_count": total}}
					}
					return response115(r, result), nil
				})
				files, err := p.List(context.Background(), a.ID, a.Root)
				if classifyError(err) != "protocol" || len(files) != 0 {
					t.Fatalf("incomplete listing accepted: count=%d kind=%s err=%v", len(files), classifyError(err), err)
				}
			})
		}
	}
}

func Test139ExplicitNullCursorCompletesOnlyValidTerminalPage(t *testing.T) {
	for _, count := range []int{0, 18, 100, 118, 200} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := setup(t)
			a := Account{ID: "pagination", Provider: "139", Root: "root", Secrets: map[string]string{
				"authorization": base64.StdEncoding.EncodeToString([]byte("fixture:123:token")), "api_host": "https://fixture.139.com",
			}}
			if err := f.s.saveAccount(a); err != nil {
				t.Fatal(err)
			}
			p := newProviders(f.s)
			offset, calls := 0, 0
			p.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				rows := []any{}
				end := min(offset+100, count)
				for i := offset; i < end; i++ {
					rows = append(rows, object{"fileId": fmt.Sprint(i), "name": fmt.Sprint(i) + ".mkv", "type": "file"})
				}
				var next any
				if end < count {
					next = "page2"
				}
				offset = end
				return response115(r, object{"success": true, "data": object{"items": rows, "nextPageCursor": next}}), nil
			})
			entries, err := p.List(context.Background(), a.ID, a.Root)
			if err != nil || len(entries) != count || calls != max(1, (count+99)/100) {
				t.Fatal("explicit terminal null rejected or extra page requested", len(entries), calls, err)
			}
		})
	}
}

package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSettingsCategoriesPreserveOtherIntegrationAndSecrets(t *testing.T) {
	f := setup(t)
	original := IntegrationConfig{Enabled: true, ServerType: "emby", Base: "http://emby.invalid:8096", APIKey: "library-fixture", LocalPrefix: "/strm", RemotePrefix: "/media", WebhookEnabled: true, Webhook: "http://hermes.invalid/webhook", WebhookSecret: "secret-fixture"}
	if err := f.s.saveIntegration(original); err != nil {
		t.Fatal(err)
	}
	_, h, cookie := testToolsServer(t, f, f.c)
	w := generationPOST(h, cookie, "/api/integrations/webhook", object{"webhook_enabled": false})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	c, _ := f.s.integrationConfig()
	if !c.Enabled || c.Base != original.Base || c.APIKey != original.APIKey || c.Webhook != original.Webhook || c.WebhookSecret != original.WebhookSecret || c.WebhookEnabled {
		t.Fatal("webhook settings overwrote library or secrets")
	}
	w = generationPOST(h, cookie, "/api/integrations/library", object{"enabled": true, "server_type": "emby", "base": "http://new-emby.invalid:8096", "refresh_all": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	c, _ = f.s.integrationConfig()
	if c.Base != "http://new-emby.invalid:8096" || c.APIKey != original.APIKey || c.WebhookEnabled || c.Webhook != original.Webhook || c.WebhookSecret != original.WebhookSecret {
		t.Fatal("library settings overwrote notification or secrets")
	}
	if strings.Contains(w.Body.String(), "fixture") {
		t.Fatal("response exposed secrets")
	}
	if generationPOST(h, cookie, "/api/integrations/unknown", object{}).Code != 404 {
		t.Fatal("invalid category accepted")
	}
}

func TestDeliveryListHasScopedContextWithoutPayloadSecrets(t *testing.T) {
	f := setup(t)
	f.s.saveJob(Job{ID: "delivery-scope", Rule: f.r.ID, SourcePath: "/media/剧集", Status: "completed", Created: 1234})
	f.e.queueDelivery("delivery-scope", "webhook", object{"title": "生成完成", "secret": "never-export-this"})
	_, h, cookie := testToolsServer(t, f, f.c)
	request := httptest.NewRequest("GET", "/api/integrations", nil)
	request.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var result struct {
		Deliveries []struct {
			Source  string `json:"source_path"`
			Created int64  `json:"created"`
			Title   string `json:"title"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Deliveries) != 1 || result.Deliveries[0].Source != "/media/剧集" || result.Deliveries[0].Created != 1234 || result.Deliveries[0].Title != "生成完成" {
		t.Fatal(w.Body)
	}
	if strings.Contains(w.Body.String(), "never-export") {
		t.Fatal("delivery payload was exported")
	}
}

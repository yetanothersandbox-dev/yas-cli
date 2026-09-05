package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

func TestIntegrationAttachmentUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial []string
		spec    string
		add     bool
		want    []string
		body    string
	}{
		{"detach last", []string{"all"}, "all", false, []string{}, `{"attach":[]}`},
		{"detach one", []string{"all", "profile:review"}, "all", false, []string{"profile:review"}, `{"attach":["profile:review"]}`},
		{"attach first", nil, "all", true, []string{"all"}, `{"attach":["all"]}`},
		{"attach another", []string{"all"}, "profile:review", true, []string{"all", "profile:review"}, `{"attach":["all","profile:review"]}`},
		{"attach existing", []string{"all"}, "all", true, []string{"all"}, `{"attach":["all"]}`},
		{"unrelated update", []string{"all"}, "", false, []string{"all"}, `{"description":"new"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := api.Integration{
				ID: "stripe", Description: "old", Service: "stripe", HasSecret: true,
				Spec: json.RawMessage(`{"upstream":"https://api.stripe.com"}`), Attach: tc.initial,
			}
			var body json.RawMessage
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/integrations/stripe" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				methods = append(methods, r.Method)
				if r.Method == http.MethodPut {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					var req api.IntegrationRequest
					if err := json.Unmarshal(body, &req); err != nil {
						t.Error(err)
					}
					if req.Attach != nil {
						stored.Attach = req.Attach
					}
					if req.Description != "" {
						stored.Description = req.Description
					}
				}
				_ = json.NewEncoder(w).Encode(stored)
			}))
			defer srv.Close()
			cl := &api.Client{BaseURL: srv.URL}
			var err error
			if tc.spec == "" {
				_, err = cl.PutIntegration(context.Background(), "stripe", api.IntegrationRequest{Description: "new"})
			} else {
				_, err = cl.AttachTo(context.Background(), "stripe", tc.spec, tc.add)
			}
			if err != nil {
				t.Fatal(err)
			}
			srv.Close()
			if string(body) != tc.body {
				t.Errorf("PUT body = %s, want %s", body, tc.body)
			}
			wantMethods := []string{http.MethodPut}
			if tc.spec != "" {
				wantMethods = append([]string{http.MethodGet}, wantMethods...)
			}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Errorf("methods = %v, want %v", methods, wantMethods)
			}
			if !reflect.DeepEqual(stored.Attach, tc.want) {
				t.Errorf("attachments = %v, want %v", stored.Attach, tc.want)
			}
			if !stored.HasSecret || stored.Service != "stripe" || string(stored.Spec) != `{"upstream":"https://api.stripe.com"}` {
				t.Errorf("unrelated settings changed: %+v", stored)
			}
			wantDescription := "old"
			if tc.spec == "" {
				wantDescription = "new"
			}
			if stored.Description != wantDescription {
				t.Errorf("description = %q, want %q", stored.Description, wantDescription)
			}
		})
	}
}

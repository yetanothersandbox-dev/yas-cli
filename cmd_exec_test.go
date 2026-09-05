package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

func TestExecDoesNotRecoverWithoutACursor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"conflict", http.StatusConflict, `{"error":"sandbox_not_live","message":"sandbox is not live"}`, "sandbox is not live"},
		{"unauthorized", http.StatusUnauthorized, `{"error":"unauthorized","message":"invalid key"}`, "invalid key"},
		{"empty stream", http.StatusOK, "", "EOF"},
		{"heartbeat only", http.StatusOK, ": ping\n\n", "EOF"},
		{"invalid first frame", http.StatusOK, "data: {broken}\n\n", "invalid character"},
		{"zero cursor", http.StatusOK, "data: {\"events\":[],\"cursor\":0}\n\n", "EOF"},
		{"connection closed", 0, "", "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				if r.Method == http.MethodPost {
					if tc.status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				fmt.Fprint(w, `{"events":[{"seq":3,"event":{"type":"exec_exit","raw":{"exitCode":0}}}],"cursor":3,"terminal":true}`)
			}))
			defer srv.Close()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("YAS_BASE_URL", srv.URL)
			t.Setenv("YAS_API_KEY", "yas_sk_test")
			err := cmdExec([]string{"box", "--", "true"})
			srv.Close()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
			if tc.status == http.StatusConflict && api.ErrorKind(err) != "sandbox_not_live" {
				t.Errorf("refusal type lost: %v", err)
			}
			if !reflect.DeepEqual(requests, []string{"POST /v1/sandboxes/box/exec?follow=1"}) {
				t.Errorf("unexpected recovery requests: %v", requests)
			}
		})
	}
}

func TestExecRecoversFromCurrentCursor(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				if r.Method == http.MethodPost {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"events\":[{\"seq\":8,\"event\":{\"type\":\"exec_output\",\"raw\":{\"data\":\"\"}}}],\"cursor\":8}\n\n")
					return
				}
				if r.URL.Query().Get("after") != "8" {
					t.Errorf("polled old history: %s", r.URL)
				}
				fmt.Fprintf(w, `{"events":[{"seq":9,"event":{"type":"exec_exit","raw":{"exitCode":%d}}}],"cursor":9,"terminal":false}`, exitCode)
			}))
			defer srv.Close()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("YAS_BASE_URL", srv.URL)
			t.Setenv("YAS_API_KEY", "yas_sk_test")
			err := cmdExec([]string{"box", "--", "true"})
			srv.Close()
			if exitCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != exitCode {
					t.Fatalf("error = %v, want exit %d", err, exitCode)
				}
			}
			want := []string{"POST /v1/sandboxes/box/exec?follow=1", "GET /v1/sandboxes/box/events?after=8"}
			if !reflect.DeepEqual(requests, want) {
				t.Errorf("requests = %v, want %v", requests, want)
			}
		})
	}
}

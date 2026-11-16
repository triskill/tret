package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func processTestFile(t *testing.T, name, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecuteDeploy(t *testing.T) {
	bpmn := `<definitions><process id="invoice"/></definitions>`
	file := processTestFile(t, "invoice.bpmn", bpmn)
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/process-definitions" || string(body) != bpmn {
					t.Errorf("unexpected deployment: %s %s %s", r.Method, r.URL, body)
				}
				if r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Error("wrong content type")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"processDefinitionKey":73}`)
			}))
			defer srv.Close()
			var out bytes.Buffer
			if err := executeDeploy([]string{"-file", file, "-engine", srv.URL}, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != "73\n" || calls != 1 {
				t.Fatalf("output=%q calls=%d", out.String(), calls)
			}
		})
	}
}

func TestExecuteStart(t *testing.T) {
	variables := processTestFile(t, "vars.json", `{"amount":42,"approved":true,"nested":{"value":"ok"}}`)
	for _, tt := range []struct {
		name            string
		args            []string
		wantKey         int64
		wantBusinessKey string
		wantVariables   map[string]any
		wantLookups     int
	}{
		{"latest arbitrary process", []string{"-process", "invoice", "-variables", variables, "-business-key", "invoice-9"}, 73, "invoice-9", map[string]any{"amount": float64(42), "approved": true, "nested": map[string]any{"value": "ok"}}, 1},
		{"pinned definition", []string{"-definition", "51"}, 51, "", map[string]any{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lookups, starts := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/process-definitions":
					lookups++
					if r.Method != http.MethodGet || r.URL.Query().Get("bpmnProcessId") != "invoice" || r.URL.Query().Get("onlyLatest") != "true" {
						t.Errorf("unexpected lookup: %s %s", r.Method, r.URL)
					}
					fmt.Fprint(w, `{"items":[{"key":73,"bpmnProcessId":"invoice"}]}`)
				case "/v1/process-instances":
					starts++
					if r.Method != http.MethodPost {
						t.Errorf("method = %s", r.Method)
					}
					var body struct {
						DefinitionKey int64          `json:"processDefinitionKey"`
						BusinessKey   string         `json:"businessKey"`
						Variables     map[string]any `json:"variables"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body.DefinitionKey != tt.wantKey || body.BusinessKey != tt.wantBusinessKey || !reflect.DeepEqual(body.Variables, tt.wantVariables) {
						t.Errorf("unexpected start body: %+v", body)
					}
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{"key":101}`)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			args := append(append([]string{}, tt.args...), "-engine", srv.URL)
			if err := executeStart(args, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != "101\n" || lookups != tt.wantLookups || starts != 1 {
				t.Fatalf("output=%q lookups=%d starts=%d", out.String(), lookups, starts)
			}
		})
	}
}

func TestProcessCommandsRejectInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name    string
		execute func([]string, io.Writer) error
		args    []string
	}{
		{"deploy missing file", executeDeploy, nil},
		{"deploy missing flag value", executeDeploy, []string{"-file"}},
		{"deploy unknown flag", executeDeploy, []string{"-unknown"}},
		{"deploy positional", executeDeploy, []string{"-file", "unused", "extra"}},
		{"deploy missing file on disk", executeDeploy, []string{"-file", filepath.Join(t.TempDir(), "missing")}},
		{"start no selector", executeStart, nil},
		{"start both selectors", executeStart, []string{"-process", "invoice", "-definition", "1"}},
		{"start empty process", executeStart, []string{"-process", ""}},
		{"start zero definition", executeStart, []string{"-definition", "0"}},
		{"start negative definition", executeStart, []string{"-definition", "-1"}},
		{"start invalid definition", executeStart, []string{"-definition", "abc"}},
		{"start positional", executeStart, []string{"-definition", "1", "extra"}},
		{"start unknown flag", executeStart, []string{"-unknown"}},
		{"start missing variables file", executeStart, []string{"-definition", "1", "-variables", filepath.Join(t.TempDir(), "missing")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tt.execute(tt.args, &out); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	for _, data := range []string{`null`, `[]`, `"text"`, `42`, `{`, `{} {}`} {
		t.Run("variables "+data, func(t *testing.T) {
			file := processTestFile(t, "vars.json", data)
			var out bytes.Buffer
			if err := executeStart([]string{"-definition", "1", "-variables", file}, &out); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestProcessCommandHelp(t *testing.T) {
	for _, execute := range []func([]string, io.Writer) error{executeDeploy, executeStart} {
		var out bytes.Buffer
		if err := execute([]string{"-h"}, &out); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help error: %v", err)
		}
		if out.Len() == 0 {
			t.Fatal("missing help output")
		}
	}
}

func TestProcessCommandsEngineFailures(t *testing.T) {
	file := processTestFile(t, "invoice.bpmn", `<definitions/>`)
	for _, tt := range []struct {
		name    string
		execute func([]string, io.Writer) error
		args    []string
		status  int
		body    string
		want    string
	}{
		{"deploy rejected", executeDeploy, []string{"-file", file}, 400, "invalid BPMN", "invalid BPMN"},
		{"deploy malformed response", executeDeploy, []string{"-file", file}, 200, "bad JSON", "parse deploy response"},
		{"lookup rejected", executeStart, []string{"-process", "invoice"}, 503, "unavailable", "unavailable"},
		{"lookup absent", executeStart, []string{"-process", "invoice"}, 200, `{"items":[]}`, "not deployed"},
		{"start rejected", executeStart, []string{"-definition", "51"}, 400, "invalid variables", "invalid variables"},
		{"start malformed response", executeStart, []string{"-definition", "51"}, 200, "bad JSON", "parse start response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			var out bytes.Buffer
			args := append(append([]string{}, tt.args...), "-engine", srv.URL)
			err := tt.execute(args, &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed success: %s", &out)
			}
		})
	}
}

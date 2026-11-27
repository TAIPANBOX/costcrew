package web

// A page is rendered whole or not at all: a template that fails part way
// answers a clean 500 and never the half of the page above the failure.

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPageThatFailsHalfWayIsNotSentHalfWritten(t *testing.T) {
	tmpl := template.Must(template.New("broken").Parse(
		`{{define "layout"}}<h1>the figures above the fault</h1>{{.Missing.Field}}{{end}}`))
	w := httptest.NewRecorder()
	(&Server{}).render(w, tmpl, struct{ Present int }{1})

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "the figures above the fault") {
		t.Errorf("the half of the page above the fault was sent: %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "could not be built") {
		t.Errorf("body %q does not say the page could not be built", w.Body.String())
	}

	ok := httptest.NewRecorder()
	good := template.Must(template.New("good").Parse(`{{define "layout"}}<p>{{.Present}}</p>{{end}}`))
	(&Server{}).render(ok, good, struct{ Present int }{7})
	if ok.Code != http.StatusOK || ok.Body.String() != "<p>7</p>" ||
		ok.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("a good page: %d %q %q", ok.Code, ok.Body.String(), ok.Header().Get("Content-Type"))
	}
}

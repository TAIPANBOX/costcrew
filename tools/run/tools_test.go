package main

import (
	"reflect"
	"sort"
	"testing"
)

// A tool exists once, in the catalogue table, and both providers see the
// same one: the Anthropic `tools` array and the OpenAI-style array
// OpenRouter reads are both rendered from it, so a tool added for one
// provider cannot silently be absent for the other. B2-SPEC.md section 3.1,
// section 4's named catalogue test.
func TestBothProvidersSeeTheSameCatalogue(t *testing.T) {
	if len(catalogue) == 0 {
		t.Fatal("the catalogue is empty")
	}

	at := anthropicTools()
	ot := openAITools()
	if len(at) != len(catalogue) || len(ot) != len(catalogue) {
		t.Fatalf("catalogue has %d tools, anthropicTools has %d, openAITools has %d",
			len(catalogue), len(at), len(ot))
	}

	anames := toolNamesFrom(at, "name")
	onames := toolNamesFromOpenAI(ot)
	sort.Strings(anames)
	sort.Strings(onames)
	if !reflect.DeepEqual(anames, onames) {
		t.Fatalf("the two renderings name different tools:\nanthropic: %v\nopenrouter: %v",
			anames, onames)
	}
	if !reflect.DeepEqual(anames, sortedToolNames()) {
		t.Fatalf("the rendering does not match the catalogue's own names:\nrendered: %v\ncatalogue: %v",
			anames, sortedToolNames())
	}

	// Every tool needs a right, a description, and required arguments, or a
	// model reading it has no way to know it exists or what to pass.
	for _, def := range catalogue {
		if def.Right == "" {
			t.Errorf("%s: no right named, so the dispatcher can never check one", def.Name)
		}
		if def.Description == "" {
			t.Errorf("%s: no description, so the model sees an empty sentence", def.Name)
		}
		req, _ := def.Schema["required"].([]string)
		if len(req) == 0 {
			t.Errorf("%s: schema names no required argument", def.Name)
		}
	}

	// The schema itself is the SAME object in both renderings, not a
	// reformatted copy: catching a divergence here means the two providers
	// cannot silently disagree about what a tool takes.
	for i, def := range catalogue {
		a := at[i]["input_schema"]
		o := ot[i]["function"].(map[string]any)["parameters"]
		if !reflect.DeepEqual(a, def.Schema) {
			t.Errorf("%s: anthropicTools' input_schema is not the catalogue's own Schema value", def.Name)
		}
		if !reflect.DeepEqual(o, def.Schema) {
			t.Errorf("%s: openAITools' parameters is not the catalogue's own Schema value", def.Name)
		}
	}
}

func toolNamesFrom(list []map[string]any, key string) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m[key].(string))
	}
	return out
}

func toolNamesFromOpenAI(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		fn := m["function"].(map[string]any)
		out = append(out, fn["name"].(string))
	}
	return out
}

// toolByName finds every catalogue entry, and refuses to find one that is
// not there: the dispatcher's "unknown tool" path is exercised by looking
// up a name that was never in the table.
func TestToolByNameFindsEveryEntryAndNothingElse(t *testing.T) {
	for _, def := range catalogue {
		if _, ok := toolByName(def.Name); !ok {
			t.Errorf("toolByName cannot find %q, which is in the catalogue", def.Name)
		}
	}
	if _, ok := toolByName("delete_everything"); ok {
		t.Error("toolByName found a tool that was never registered")
	}
}

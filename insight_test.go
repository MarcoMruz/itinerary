package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInsightsNormalizeAndValidate(t *testing.T) {
	it := Itinerary{
		Title: "Trip",
		Days: []Day{{Activities: []Activity{
			{Time: "9", Title: "Lake", Insight: &Insight{Summary: " Come early ", Tips: []string{" Park at the north lot ", " "}, Sources: []string{"https://example.com"}}},
			{Time: "12", Title: "Lunch", Insight: &Insight{Tips: []string{"  "}}},
		}}},
		LocalGuide: &LocalGuide{GettingAround: []string{" Buy the day pass "}, Avoid: []string{""}},
	}
	if err := it.Normalize(); err != nil {
		t.Fatal(err)
	}
	in := it.Days[0].Activities[0].Insight
	if in.Summary != "Come early" || len(in.Tips) != 1 || in.Tips[0] != "Park at the north lot" {
		t.Fatalf("insight not cleaned: %+v", in)
	}
	if it.Days[0].Activities[1].Insight != nil {
		t.Fatal("blank insight must be dropped")
	}
	if it.LocalGuide == nil || it.LocalGuide.GettingAround[0] != "Buy the day pass" || it.LocalGuide.Avoid != nil {
		t.Fatalf("guide not cleaned: %+v", it.LocalGuide)
	}

	tooMany := make([]string, maxInsightItems+1)
	for _, bad := range []Itinerary{
		{Title: "x", Days: []Day{{Activities: []Activity{{Time: "9", Title: "a", Insight: &Insight{Summary: "s", Sources: []string{"javascript:alert(1)"}}}}}}},
		{Title: "x", Days: []Day{{Activities: []Activity{{Time: "9", Title: "a", Insight: &Insight{Tips: tooMany}}}}}},
		{Title: "x", Days: []Day{{Activities: []Activity{}}}, LocalGuide: &LocalGuide{Summary: strings.Repeat("a", maxTextRunes+1)}},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("accepted invalid insight: %+v", bad)
		}
	}
}

func TestCloneDoesNotShareInsights(t *testing.T) {
	it := &Itinerary{
		Days:       []Day{{Activities: []Activity{{Insight: &Insight{Tips: []string{"a"}}}}}},
		LocalGuide: &LocalGuide{Tips: []string{"b"}},
	}
	c := cloneItinerary(it)
	c.Days[0].Activities[0].Insight.Tips[0] = "changed"
	c.LocalGuide.Tips[0] = "changed"
	if it.Days[0].Activities[0].Insight.Tips[0] != "a" || it.LocalGuide.Tips[0] != "b" {
		t.Fatal("clone shares insight data with the original")
	}
}

func TestInsiderExportAndGuideUpdate(t *testing.T) {
	srv, _ := newTestServer(t, "")
	api := srv.URL + "/api/v1"
	instructions := func(url string) string {
		_, body := do(t, "GET", url, "")
		var doc struct{ Instructions string }
		json.Unmarshal(body, &doc)
		return doc.Instructions
	}
	if strings.Contains(instructions(api+"/itineraries/slovensky-raj-1yo/export"), "LOCAL INSIDER") {
		t.Fatal("plain export must not ask for research")
	}
	if !strings.Contains(instructions(api+"/itineraries/slovensky-raj-1yo/export?insider=true"), "LOCAL INSIDER") {
		t.Fatal("insider export misses its instructions")
	}

	_, body := do(t, "GET", api+"/import/template?insider=true", "")
	if !strings.Contains(string(body), `"localGuide"`) || !strings.Contains(string(body), `"insight"`) {
		t.Fatalf("insider template lacks examples: %s", body)
	}
	_, plain := do(t, "GET", api+"/import/template", "")
	var plainDoc struct{ Itinerary json.RawMessage }
	json.Unmarshal(plain, &plainDoc)
	if strings.Contains(string(plainDoc.Itinerary), "insight") || strings.Contains(string(plainDoc.Itinerary), "localGuide") {
		t.Fatal("plain template must not include insight examples")
	}
	code, created := do(t, "POST", api+"/import", string(body))
	if code != 201 || !strings.Contains(string(created), `"gettingAround"`) {
		t.Fatalf("insider template import: %d %s", code, created)
	}

	url := api + "/itineraries/slovensky-raj-1yo"
	if code, b := do(t, "PATCH", url, `{"localGuide":{"tips":["Cash only at huts"]}}`); code != 200 || !strings.Contains(string(b), "Cash only") {
		t.Fatalf("set guide: %d %s", code, b)
	}
	if code, b := do(t, "PATCH", url, `{"localGuide":{"avoid":["Weekends"]}}`); code != 200 || strings.Contains(string(b), "Cash only") {
		t.Fatalf("guide must be replaced, not merged: %d %s", code, b)
	}
	if code, b := do(t, "PATCH", url, `{"localGuide":{}}`); code != 200 || strings.Contains(string(b), "localGuide") {
		t.Fatalf("clear guide: %d %s", code, b)
	}
}

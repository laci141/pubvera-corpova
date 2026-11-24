package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseSynthesisStudyStances pins the per-study AI stance list: kept as
// returned when valid, normalized when not, and absent (not an empty array)
// when the model sent none, so older clients see the same payload.
func TestParseSynthesisStudyStances(t *testing.T) {
	raw := `{"stance":"supports","confidence":0.8,"reasoning":"r","key_evidence":[],"excluded_studies":[],
	"study_stances":[
	 {"title":" Study One ","doi":" 10.1/a ","claim":"","stance":"supports","reason":" walk distance +32 m "},
	 {"title":"Study Two","stance":"Refutes","claim":"b","reason":"no change in exercise"},
	 {"title":"Study Three","stance":"probably yes","claim":"C"},
	 {"title":"   ","stance":"supports"}
	]}`
	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := syn.StudyStances
	if len(got) != 3 {
		t.Fatalf("len(study_stances) = %d, want 3 (empty title dropped): %+v", len(got), got)
	}
	want := []studyStance{
		{Title: "Study One", DOI: "10.1/a", Stance: "supports", Reason: "walk distance +32 m"},
		{Title: "Study Two", Claim: "B", Stance: "refutes", Reason: "no change in exercise"},
		{Title: "Study Three", Stance: "unclear"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("study_stances[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseSynthesisStudyStancesAbsentStaysOmitted(t *testing.T) {
	syn, err := parseSynthesis(`{"stance":"mixed","confidence":0.5,"reasoning":"r","key_evidence":[],"excluded_studies":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(syn)
	if strings.Contains(string(b), "study_stances") {
		t.Errorf("payload carries study_stances although the model sent none: %s", b)
	}
}

func TestParseSynthesisStudyStancesCapped(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"stance":"mixed","confidence":0.5,"reasoning":"r","key_evidence":[],"excluded_studies":[],"study_stances":[`)
	for i := 0; i < maxStudyStances+15; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"title":"T","stance":"supports","reason":"` + strings.Repeat("x", 500) + `"}`)
	}
	sb.WriteString("]}")
	syn, err := parseSynthesis(sb.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(syn.StudyStances) != maxStudyStances {
		t.Errorf("len = %d, want cap %d", len(syn.StudyStances), maxStudyStances)
	}
	if l := len(syn.StudyStances[0].Reason); l != 200 {
		t.Errorf("reason length = %d, want 200", l)
	}
}

// TestSynthesisPromptAsksForStudyStances pins the instruction the feature
// depends on: without it the model never fills the list.
func TestSynthesisPromptAsksForStudyStances(t *testing.T) {
	p := synthesisPrompt("consensus", []string{"claim"}, []byte(`{}`))
	for _, want := range []string{
		`"study_stances":[`,
		`supports|refutes|mixed|unclear`,
		"ONE entry for EVERY study you kept",
		"judged ONLY from that study's own abstract",
		"Never list an excluded study in study_stances",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestAnthropicMaxTokensRoomForStudyStances(t *testing.T) {
	if anthropicMaxTokens < 8192 {
		t.Errorf("anthropicMaxTokens = %d, want >= 8192 (study_stances adds one entry per kept study)", anthropicMaxTokens)
	}
}

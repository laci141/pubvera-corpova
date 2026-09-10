package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The bug these tests exist for, measured on a live compare run (2026-09-09,
// deepseek-chat): the model put "Using probiotics to improve swine gut health
// and nutrient utilization" into excluded_studies with the reason
// "Animal-only study (pigs), not directly applicable to human gut health
// claim." — a genuine exclusion — and then cited that same study in
// key_evidence as support for the mechanism. The prompt forbids this
// explicitly; the model did it anyway. Neither list is wrong on its own, so
// nothing may be deleted; the contradiction is only visible by comparing them,
// and the user has to be told.

// hasExcluded reports whether the exclusion list still carries the title.
func hasExcluded(list []excludedStudy, title string) bool {
	for _, e := range list {
		if e.Title == title {
			return true
		}
	}
	return false
}

// hasEvidenceContaining reports whether some key_evidence point contains the
// substring — used to prove the cited point survived the check untouched.
func hasEvidenceContaining(l keyEvidenceList, sub string) bool {
	for _, p := range l.Points() {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

// TestContradictionByDOIIsReportedAndNothingIsRemoved is case 1 and the most
// important test in the file. The key_evidence sentence deliberately shares no
// distinctive words with the excluded title, so only the DOI can link them —
// and BOTH lists must come out of parseSynthesis exactly as they went in. A
// check that "fixes" the contradiction by dropping one side is the regression
// this asserts against: it would put a difference between what the model said
// and what the user sees, with nothing on the screen to say so.
func TestContradictionByDOIIsReportedAndNothingIsRemoved(t *testing.T) {
	raw := `{
	  "stance": "supports",
	  "confidence": 0.7,
	  "reasoning": "Probiotics show a consistent effect on gut barrier markers.",
	  "key_evidence": [
	    {"point": "A 2017 narrative review in Animal Nutrition states that dietary probiotics improve gut health and nutrient digestibility in pigs, supporting the mechanism in animals.",
	     "doi": "10.1016/j.aninu.2017.06.007"},
	    {"point": "A 2020 human RCT reported a 12% reduction in intestinal permeability.",
	     "doi": "10.1000/human-rct"}
	  ],
	  "excluded_studies": [
	    {"title": "Using probiotics to improve swine gut health and nutrient utilization",
	     "doi": "10.1016/j.aninu.2017.06.007",
	     "reason": "Animal-only study (pigs), not directly applicable to human gut health claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}

	const title = "Using probiotics to improve swine gut health and nutrient utilization"
	if !slices.Contains(syn.Contradictions, title) {
		t.Errorf("contradictions = %v, want the swine study reported", syn.Contradictions)
	}

	// No-removal guarantee, asserted on both sides.
	if !hasExcluded(syn.ExcludedStudies, title) {
		t.Errorf("the excluded entry was removed; excluded_studies = %+v", syn.ExcludedStudies)
	}
	if !hasEvidenceContaining(syn.KeyEvidence, "nutrient digestibility in pigs") {
		t.Errorf("the cited key_evidence point was removed; key_evidence = %v", syn.KeyEvidence.Points())
	}
	if len(syn.KeyEvidence) != 2 {
		t.Errorf("key_evidence has %d points, want 2 — the check must not edit the list", len(syn.KeyEvidence))
	}
	if syn.Stance != "supports" {
		t.Errorf("stance = %q, want %q — the check must not change the verdict", syn.Stance, "supports")
	}
	if syn.Confidence != 0.7 {
		t.Errorf("confidence = %v, want 0.7 — the check must not change the confidence", syn.Confidence)
	}
}

// TestContradictionByTitleFallback is case 2: the live shape, where neither
// side carries a DOI and the only link is the title text inside the point.
// This is the path that has to work on its own, because a model that ignores
// the instruction at the end of the prompt will also ignore the doi field.
func TestContradictionByTitleFallback(t *testing.T) {
	raw := `{
	  "stance": "mixed",
	  "confidence": 0.5,
	  "reasoning": "Evidence in humans is thinner than the animal literature suggests.",
	  "key_evidence": [
	    "Using probiotics to improve swine gut health and nutrient utilization reports improved nutrient digestibility, supporting the mechanism."
	  ],
	  "excluded_studies": [
	    {"title": "Using probiotics to improve swine gut health and nutrient utilization",
	     "reason": "Animal-only study (pigs), not directly applicable to human gut health claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	if len(syn.Contradictions) != 1 {
		t.Fatalf("contradictions = %v, want exactly the swine study", syn.Contradictions)
	}
	if !hasExcluded(syn.ExcludedStudies, syn.Contradictions[0]) {
		t.Error("the reported study is no longer in excluded_studies")
	}
}

// TestGenuineExclusionIsNotReported is case 3. An exclusion the model never
// cites is the normal, correct case; warning on it would train the user to
// ignore the warning.
func TestGenuineExclusionIsNotReported(t *testing.T) {
	raw := `{
	  "stance": "supports",
	  "confidence": 0.8,
	  "reasoning": "Human trials are consistent.",
	  "key_evidence": [
	    {"point": "A 2020 human RCT reported a 12% reduction in intestinal permeability.", "doi": "10.1000/human-rct"}
	  ],
	  "excluded_studies": [
	    {"title": "Using probiotics to improve swine gut health and nutrient utilization",
	     "doi": "10.1016/j.aninu.2017.06.007",
	     "reason": "Animal-only study (pigs), not directly applicable to human gut health claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	if len(syn.Contradictions) != 0 {
		t.Errorf("contradictions = %v, want none: the excluded study is cited nowhere", syn.Contradictions)
	}
	if len(syn.ExcludedStudies) != 1 {
		t.Errorf("excluded_studies = %+v, want the one genuine exclusion", syn.ExcludedStudies)
	}
}

// TestSharedWordsAreNotAContradiction is case 4: the guard on the title
// fallback. The point cites a different study that happens to share most of
// the excluded title's vocabulary. Word overlap is not a citation, and the
// fallback must require the whole title, not its words.
func TestSharedWordsAreNotAContradiction(t *testing.T) {
	raw := `{
	  "stance": "mixed",
	  "confidence": 0.4,
	  "reasoning": "Human data are limited.",
	  "key_evidence": [
	    "A 2019 randomized trial of a probiotic supplement found a 1.3 kg difference in body weight in adults."
	  ],
	  "excluded_studies": [
	    {"title": "Effects of a probiotic supplement on body weight in laboratory rats",
	     "reason": "Animal model only; cannot support a human body-weight claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	if len(syn.Contradictions) != 0 {
		t.Errorf("contradictions = %v, want none: the point cites a different study", syn.Contradictions)
	}
}

// TestOldShapeKeyEvidenceParses is case 5. The bare-string array is what every
// response used before the schema asked for DOIs, and what a model that
// ignores the new schema will keep returning. It must parse, keep its text,
// and still be searchable by the title fallback.
func TestOldShapeKeyEvidenceParses(t *testing.T) {
	raw := `{
	  "stance": "supports",
	  "confidence": 0.9,
	  "reasoning": "stub",
	  "key_evidence": ["first point", "Using probiotics to improve swine gut health and nutrient utilization reports better digestibility."],
	  "excluded_studies": [
	    {"title": "Using probiotics to improve swine gut health and nutrient utilization",
	     "reason": "Animal-only study (pigs), not directly applicable to human gut health claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	got := syn.KeyEvidence.Points()
	if len(got) != 2 || got[0] != "first point" {
		t.Fatalf("key_evidence points = %v, want the two strings unchanged", got)
	}
	for _, p := range syn.KeyEvidence {
		if p.DOI != "" {
			t.Errorf("string entry got a DOI from nowhere: %q", p.DOI)
		}
	}
	if len(syn.Contradictions) != 1 {
		t.Errorf("contradictions = %v, want the title fallback to fire on the old shape", syn.Contradictions)
	}
}

// TestEmptyDOIsAreNeverEqual is case 6, and the bug most likely to be written
// by accident: comparing two absent DOIs as strings makes every excluded study
// "match" every key_evidence point. An absent identifier is unknown, not a
// value. The placeholder row ("n/a" on both sides) is here because that is
// what a model writes into a field it has nothing for.
func TestEmptyDOIsAreNeverEqual(t *testing.T) {
	raw := `{
	  "stance": "supports",
	  "confidence": 0.6,
	  "reasoning": "Human trials are consistent.",
	  "key_evidence": [
	    {"point": "A 2020 human RCT reported a 12% reduction in intestinal permeability.", "doi": ""},
	    {"point": "A 2021 meta-analysis of eight trials found a small but consistent benefit.", "doi": "n/a"},
	    {"point": "Using probiotics to improve swine gut health and nutrient utilization reports better digestibility.", "doi": ""}
	  ],
	  "excluded_studies": [
	    {"title": "Ultra-processed food intake and cardiovascular mortality in France", "doi": "", "reason": "Off-topic: different exposure and outcome."},
	    {"title": "In-vitro fermentation of inulin by human faecal microbiota", "doi": "n/a", "reason": "In-vitro only; cannot support a clinical claim."},
	    {"title": "Using probiotics to improve swine gut health and nutrient utilization", "doi": "", "reason": "Animal-only study (pigs), not directly applicable to human gut health claim."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	if len(syn.KeyEvidence) != 3 {
		t.Fatalf("key_evidence has %d points, want 3", len(syn.KeyEvidence))
	}
	// Exactly one contradiction, found by title — not three, which is what an
	// "" == "" DOI comparison would report.
	want := []string{"Using probiotics to improve swine gut health and nutrient utilization"}
	if len(syn.Contradictions) != 1 || syn.Contradictions[0] != want[0] {
		t.Errorf("contradictions = %v, want %v", syn.Contradictions, want)
	}
}

// TestContradictionsCappedAtFive is case 7. key_evidence itself is capped at 5
// points, so the sixth study is reached through a point that names two of
// them; the cap on the warning has to hold independently of the cap on the
// list it reads.
func TestContradictionsCappedAtFive(t *testing.T) {
	titles := []string{
		"Probiotic supplementation in weanling piglets and feed conversion",
		"Dietary fibre fermentation in the porcine caecum under stress",
		"In-vitro adhesion of lactobacilli to intestinal epithelial cells",
		"Rodent models of antibiotic-associated dysbiosis and recovery",
		"Broiler chicken performance under synbiotic supplementation",
		"Cell-culture assay of tight junction protein expression",
	}
	points := []string{
		// One point, two studies: six contradictions out of five points.
		titles[0] + " and " + titles[1] + " both report improved nutrient use.",
		titles[2] + " shows adhesion.",
		titles[3] + " shows recovery.",
		titles[4] + " shows performance gains.",
		titles[5] + " shows barrier effects.",
	}

	type ex struct {
		Title  string `json:"title"`
		Reason string `json:"reason"`
	}
	payload := struct {
		Stance     string   `json:"stance"`
		Confidence float64  `json:"confidence"`
		Reasoning  string   `json:"reasoning"`
		Key        []string `json:"key_evidence"`
		Excluded   []ex     `json:"excluded_studies"`
	}{Stance: "mixed", Confidence: 0.5, Reasoning: "r", Key: points}
	for _, tl := range titles {
		payload.Excluded = append(payload.Excluded, ex{Title: tl, Reason: "Animal or in-vitro only."})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("building fixture: %v", err)
	}

	syn, err := parseSynthesis(string(raw))
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}
	if len(syn.Contradictions) != maxContradictions {
		t.Fatalf("contradictions = %d, want the cap of %d: %v", len(syn.Contradictions), maxContradictions, syn.Contradictions)
	}
	// Capping the report must not cap the lists it reports on.
	if len(syn.ExcludedStudies) != len(titles) {
		t.Errorf("excluded_studies has %d entries, want all %d", len(syn.ExcludedStudies), len(titles))
	}
}

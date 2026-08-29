package main

import "testing"

// TestSelfContradictingExclusionOnLiveReasons uses the eleven reasons DeepSeek
// returned for one comparison of "intermittent fasting extends lifespan" against
// "statins reduce heart attack risk". Five of them say the study was kept while
// filing it as excluded; six are genuine exclusions.
//
// Real strings rather than invented ones: the failure this guards against is a
// model's phrasing, and a hand-written fixture would be testing the phrasing the
// author imagined instead of the one that shipped.
func TestSelfContradictingExclusionOnLiveReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		// Filed as excluded, reason says kept. The first of these is the West of
		// Scotland trial, which the same response quotes in key_evidence for a
		// 31% risk reduction — and which vanished from the supporting column.
		{"This is a key RCT for statin claim, kept.", true},
		{"This RCT showed no significant benefit of pravastatin on all-cause mortality, but it is relevant to statin claim; kept as evidence but noted as refuting.", true},
		{"This study is relevant to health markers but not lifespan; kept for context but not used for lifespan claim.", true},
		{"This is an RCT on stroke prevention, not directly on heart attack risk; but it is relevant to cardiovascular risk reduction, so kept for statin claim.", true},
		{"Narrative review, but relevant to statin efficacy; kept for context.", true},

		// Genuine exclusions. None may be flagged: dropping one of these would
		// put an off-topic study back into the columns, which is the opposite of
		// what this filter is for.
		{"Statistical update, not a study on statin efficacy; excluded as off-topic.", false},
		{"Off-topic: focuses on brain health and neuroplasticity, not lifespan extension.", false},
		{"Animal model and mechanistic review, not human lifespan data.", false},
		{"Animal model (C. elegans) only, not applicable to humans.", false},
		{"This study is about niacin added to statins, not statins alone; excluded as different substance.", false},
		{"This is a meta-analysis of a polypill, not statins alone; excluded as different intervention.", false},
	}

	for _, c := range cases {
		if got := selfContradictingExclusion(c.reason); got != c.want {
			verb := "flagged"
			if c.want {
				verb = "missed"
			}
			t.Errorf("%s: %q", verb, c.reason)
		}
	}
}

// TestSelfContradictingExclusionEdges pins the boundary the narrow vocabulary
// buys. "not used" and "cannot be used" are what a real exclusion says, and a
// wider pattern built around "use" would swallow them.
func TestSelfContradictingExclusionEdges(t *testing.T) {
	shouldNotFlag := []string{
		"Not used: the abstract reports no outcome relevant to the claim.",
		"Cannot be used for a human claim; the sample is rats.",
		"Off-topic.",
		"",
	}
	for _, r := range shouldNotFlag {
		if selfContradictingExclusion(r) {
			t.Errorf("flagged a genuine exclusion: %q", r)
		}
	}

	shouldFlag := []string{
		"Kept for context.",
		"RETAINED as supporting evidence.",
		"Weak design but still used in the synthesis.",
	}
	for _, r := range shouldFlag {
		if !selfContradictingExclusion(r) {
			t.Errorf("missed a self-contradiction: %q", r)
		}
	}
}

// TestParseSynthesisDropsSelfContradictingExclusions is the wiring test. The
// filter above is a pure function, so a test of it alone stays green even if the
// call site is deleted — verified by mutation: commenting out the call in
// parseSynthesis left every other test in this file passing.
//
// The fixture is a trimmed but otherwise unaltered DeepSeek response from the
// statins/fasting comparison: one genuine exclusion and one that says kept.
func TestParseSynthesisDropsSelfContradictingExclusions(t *testing.T) {
	raw := `{
	  "stance": "supports",
	  "confidence": 0.82,
	  "reasoning": "High-quality human RCTs show statins reduce heart attack risk.",
	  "key_evidence": [
	    "The West of Scotland Coronary Prevention Study showed a 31% relative risk reduction."
	  ],
	  "excluded_studies": [
	    {"title": "Prevention of Coronary Heart Disease with Pravastatin in Men with Hypercholesterolemia",
	     "reason": "This is a key RCT for statin claim, kept."},
	    {"title": "Heart Disease and Stroke Statistics-2014 Update",
	     "reason": "Statistical update, not a study on statin efficacy; excluded as off-topic."}
	  ]
	}`

	syn, err := parseSynthesis(raw)
	if err != nil {
		t.Fatalf("parseSynthesis: %v", err)
	}

	if len(syn.ExcludedStudies) != 1 {
		t.Fatalf("kept %d exclusions, want 1:\n%+v", len(syn.ExcludedStudies), syn.ExcludedStudies)
	}
	got := syn.ExcludedStudies[0].Title
	if got != "Heart Disease and Stroke Statistics-2014 Update" {
		t.Errorf("surviving exclusion = %q, want the statistics update", got)
	}

	// The Pravastatin trial is the study key_evidence quotes. It must not be in
	// the exclusion list, because the UI removes excluded studies from the
	// supporting column — which is how the strongest evidence for the claim came
	// to be missing from the screen while being cited in the summary above it.
	for _, e := range syn.ExcludedStudies {
		if e.Title == "Prevention of Coronary Heart Disease with Pravastatin in Men with Hypercholesterolemia" {
			t.Error("the trial cited in key_evidence is still listed as excluded")
		}
	}
}

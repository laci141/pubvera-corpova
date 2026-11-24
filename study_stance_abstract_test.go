package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUnclearWhenNoAbstract pins the guard measured on 2026-11-24: a model
// judged a study with an empty abstract ("supports", "Abstract indicates…").
func TestUnclearWhenNoAbstract(t *testing.T) {
	cli := []byte(`{"all_studies":[
	 {"title":"Exercise Performance Improves in Patients With COPD","doi":"10.1378/chest.129.4.886","stance":"supporting","abstract":""},
	 {"title":"No DOI and no abstract field here","stance":"supporting"},
	 {"title":"Has an abstract","doi":"10.1/full","stance":"supporting","abstract":"6MWD +32 m"},
	 {"title":"Twice listed study","doi":"10.1/twice","stance":"supporting","abstract":""}],
	 "top_supporting":[{"title":"Twice listed study","doi":"10.1/twice","stance":"supporting","abstract":"real text"}]}`)
	in := []studyStance{
		{Title: "Exercise Performance Improves in Patients With COPD", DOI: "https://doi.org/10.1378/CHEST.129.4.886", Stance: "supports", Reason: "Abstract indicates improvement"},
		{Title: "No DOI and no abstract field here!", Stance: "refutes", Reason: "x"},
		{Title: "Has an abstract", DOI: "10.1/full", Stance: "supports", Reason: "kept"},
		{Title: "Twice listed study", DOI: "10.1/twice", Stance: "mixed", Reason: "kept too"},
		{Title: "Not in the tool output", Stance: "supports", Reason: "kept, unknown study"},
	}
	got := unclearWhenNoAbstract(in, cli)
	want := []studyStance{
		{Title: in[0].Title, DOI: in[0].DOI, Stance: "unclear", Reason: "no abstract available to judge"},
		{Title: in[1].Title, Stance: "unclear", Reason: "no abstract available to judge"},
		in[2], in[3], in[4],
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if in[0].Stance != "supports" {
		t.Error("input slice was modified")
	}
	if r := unclearWhenNoAbstract(in, []byte("not json")); r[0].Stance != "supports" {
		t.Error("bad CLI JSON must leave the list unchanged")
	}
}

// TestLLMSynthesizeAppliesNoAbstractGuard pins the call site: the guard must
// run on the real synthesis path, not only exist.
func TestLLMSynthesizeAppliesNoAbstractGuard(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stance\":\"supports\",\"confidence\":0.9,\"reasoning\":\"ok\",\"key_evidence\":[],\"study_stances\":[{\"title\":\"Empty abstract study\",\"doi\":\"10.1/e\",\"stance\":\"supports\",\"reason\":\"Abstract indicates\"}]}"}}]}`))
	}))
	defer llm.Close()
	useFakeProvider(t, "openai", llm.URL)
	cli := []byte(`{"all_studies":[{"title":"Empty abstract study","doi":"10.1/e","stance":"supporting","abstract":""}]}`)
	syn, err := llmSynthesize(context.Background(), "openai", "k", "", "consensus", []string{"c"}, cli)
	if err != nil {
		t.Fatalf("llmSynthesize: %v", err)
	}
	if len(syn.StudyStances) != 1 || syn.StudyStances[0].Stance != "unclear" {
		t.Errorf("study_stances = %+v, want one entry forced to unclear", syn.StudyStances)
	}
}

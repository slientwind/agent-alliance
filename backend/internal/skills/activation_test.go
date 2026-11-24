package skills

import (
	"reflect"
	"testing"
)

func TestActivationUsesOnlyBoundRootsAndTheirFixedDependencies(t *testing.T) {
	shared := Ref{ID: "shared", Revision: 2}
	quote := Ref{ID: "quote", Revision: 3}
	ledger := Ref{ID: "ledger", Revision: 1}
	style := Ref{ID: "style", Revision: 1}
	catalog := []pinnedSkill{
		{Ref: shared, Name: "base", Allowed: true, Terms: []string{"无关术语"}},
		{Ref: quote, Name: "quote", Allowed: true, Root: true, Terms: []string{"供应商报价"}, Dependencies: []Ref{shared}},
		{Ref: ledger, Name: "ledger", Allowed: true, Root: true, Terms: []string{"对账"}, Dependencies: []Ref{shared}},
		{Ref: style, Name: "style", Allowed: true, Root: true},
	}
	for _, tc := range []struct {
		input string
		want  []Ref
	}{
		{"整理会议记录", []Ref{style}},
		{"核对三家供应商报价", []Ref{shared, quote, style}},
		{"请用 @LEDGER 核对月报", []Ref{shared, ledger, style}},
		{"对账并核对供应商报价", []Ref{shared, quote, ledger, style}},
		{"联系 test@quote，暂不使用 @ledger-preview", []Ref{style}},
		{"@base @unknown", []Ref{style}},
	} {
		got, err := selectBindings(catalog, &tc.input)
		var refs []Ref
		for _, skill := range got {
			refs = append(refs, skill.Ref)
		}
		if err != nil || !reflect.DeepEqual(refs, tc.want) {
			t.Errorf("%q: %v, want %v, err=%v", tc.input, refs, tc.want, err)
		}
	}
	if _, err := selectBindings(catalog[1:], new("供应商报价")); err == nil {
		t.Fatal("missing declared dependency was accepted")
	}
	catalog[2].Allowed = false
	if _, err := selectBindings(catalog, new("整理会议记录")); err == nil {
		t.Fatal("an invalid binding was treated as an admitted revision")
	}
}

func TestSkillActivationTermsAreValidatedAndCoveredByTheDigest(t *testing.T) {
	main := packageFile{Path: entry, Text: sample}
	pack, err := readPackage(skillZIP(t, main, packageFile{Path: manifestName,
		Text: `{"activationTerms":["  报价  ","BUDGET Review"]}`}))
	if err != nil || !reflect.DeepEqual(pack.ActivationTerms, []string{"报价", "budget review"}) {
		t.Fatalf("activation metadata: %v %v", pack.ActivationTerms, err)
	}
	next, err := readPackage(skillZIP(t, main, packageFile{Path: manifestName, Text: `{"activationTerms":["报价","对账"]}`}))
	if err != nil || next.Digest == pack.Digest {
		t.Fatalf("modified triggers did not change the package digest: %v", err)
	}
	for _, manifest := range []string{
		`{"activationTerms":["  "]}`,
		`{"activationTerms":["Quote","quote"]}`,
		`{"activationTerms":["x\ny"]}`,
		`{"activationTerms":["a","b","c","d","e","f","g","h","i"]}`,
	} {
		if _, err := readPackage(skillZIP(t, main, packageFile{Path: manifestName, Text: manifest})); err == nil {
			t.Errorf("invalid activation accepted: %s", manifest)
		}
	}
}

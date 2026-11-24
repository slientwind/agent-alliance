package skills

import (
	"reflect"
	"testing"
)

func TestSkillGraphPinsDependenciesOnceAndRejectsVersionConflicts(t *testing.T) {
	a := Ref{"00000001-0000-4000-8000-000000000001", 1}
	b := Ref{"00000001-0000-4000-8000-000000000002", 1}
	c := Ref{"00000001-0000-4000-8000-000000000003", 2}
	graph := map[Ref]dependencyNode{
		a: {[]Ref{c}, 128}, b: {[]Ref{c}, 256}, c: {nil, 1000},
	}
	get := func(ref Ref) (dependencyNode, error) { return graph[ref], nil }
	order, size, err := resolveBindings([]Ref{a, b}, "", get)
	if err != nil || size != 1384 || !reflect.DeepEqual(order, []Ref{c, a, b}) {
		t.Fatalf("compiled %v, bytes %d, error %v", order, size, err)
	}
	graph[b] = dependencyNode{[]Ref{{c.ID, 3}}, 256}
	if _, _, err := resolveBindings([]Ref{a, b}, "", get); err == nil {
		t.Fatal("accepted two revisions of the same dependency")
	}
	if _, _, err := resolveBindings([]Ref{a}, c.ID, get); err == nil {
		t.Fatal("accepted a transitive dependency on the pack being published")
	}
	graph[c] = dependencyNode{[]Ref{a}, 128}
	if _, _, err := resolveBindings([]Ref{a}, "", get); err == nil {
		t.Fatal("accepted a cycle")
	}
	graph[c] = dependencyNode{nil, contextLimit}
	if _, _, err := resolveBindings([]Ref{a}, "", get); err == nil {
		t.Fatal("accepted an oversized context")
	}
}

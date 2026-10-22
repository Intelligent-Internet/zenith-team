package wiki

import (
	"errors"
	"strings"
	"testing"
)

func TestSourceNestedPropertyLocations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, source, selected string }{
		{"option label", `<DataTable data={"op":"literal","value":[]} columns=[{"key":"状態","label":123,"type":"string"}] />`, "123"},
		{"duplicate option", `<Select label="Status" bind="status" options=["a","a"] />`, `"a"`},
		{"source field", `<Scope name="sample" initial={} sources={"tasks":{"type":"tasks","projectId":"invalid"}}>`, `"invalid"`},
		{"action variant", `<Button label="Next" action={"type":"unknown"} />`, `"unknown"`},
		{"action nested", `<Button label="Next" action={"type":"navigate","href":"http://example.com"} />`, `"http://example.com"`},
		{"unknown field", `<Button label="Next" action={"type":"copy","text":"a","extra":42} />`, "42"},

		{"expression argument", `<Metric label="結果" value={"op":"add","args":[{"op":"literal","value":1},{"op":"state","name":"bad-name"}]} />`, `"bad-name"`},
		{"expression body", `<Metric label="Result" value={"op":"map","input":{"op":"literal","value":[]},"body":{"op":"field","input":{"op":"item"},"field":"__proto__"}} />`, `"__proto__"`},
		{"expression property", `<Metric label="Result" value={"op":"literal","value":1,"unknown":42} />`, `42`},
		{"computed expression", `<Scope name="sample" initial={} computed={"total":{"op":"add","args":[{"op":"literal","value":1},{"op":"unknown"}]}}>`, `"unknown"`},
		{"wrapped computed expression", `<Scope name="sample" initial={} computed={{"total":{"op":"state","name":"bad-name"}}}>`, `"bad-name"`},
		{"native child field", `<Native node={"type":"paragraph","content":[{"type":"text","text":"é","unknown":42}]} />`, `42`},
		{"native text type", `<Native node={"type":"paragraph","content":[{"type":"text","text":123}]} />`, `123`},
		{"native mark field", `<Native node={"type":"paragraph","content":[{"type":"text","text":"é","marks":[{"type":"bold","unknown":false}]}]} />`, `false`},
		{"native attribute shape", `<Native node={"type":"image","attrs":[1]} />`, `[1]`},
		{"wrapped native attribute shape", `<Native node={{"type":"image","attrs":[1]}} />`, `[1]`},
		{"wrapped native text", `<Native node={ {"type":"paragraph","content":[{"type":"text","text":123}]} } />`, `123`},
		{"wrapped options", `<Select label="Status" bind="status" options={["a","a"]} />`, `"a"`},
		{"wrapped columns", `<DataTable data=[] columns={[{"key":"状態","label":123,"type":"string"}]} />`, `123`},
		{"wrapped initial", `<Scope name="sample" initial={{"amount":{"invalid":true}}}>`, `{"invalid":true}`},
		{"native document", `<Native node={"type":"doc"} />`, `"doc"`},
		{"native children shape", `<Native node={"type":"paragraph","content":42} />`, `42`},
		{"native deterministic properties", `<Native node={"type":"uiMetric","attrs":{"label":"Result","value":{"op":"literal","value":1},"zUnknown":42,"aUnknown":123}} />`, `123`},
		{"native catalog property", `<Native node={"type":"uiMetric","attrs":{"label":"結果","value":{"op":"literal","value":1},"unknown":42}} />`, `42`},
		{"native catalog version", `<Native node={"type":"uiMetric","attrs":{"label":"Result","value":{"op":"literal","value":1},"catalogVersion":2}} />`, `2`},
		{"native nested expression", `<Native node={"type":"paragraph","content":[{"type":"uiValue","attrs":{"value":{"op":"state","name":"bad-name"}}}]} />`, `"bad-name"`},
		{"native wrapped catalog", `<Native node={{"type":"uiDataTable","attrs":{"label":"Results","data":{"op":"literal","value":[]},"columns":[{"key":"状態","label":123,"type":"string"}]}}} />`, `123`},
		{"native constraint", `<Native node={"type":"uiSlider","attrs":{"label":"Amount","bind":"amount","min":10,"max":2}} />`, `2`},
		{"native content", `<Native node={"type":"uiMetric","attrs":{"label":"Result","value":{"op":"literal","value":1}},"text":"invalid"} />`, `"invalid"`},
		{"native missing property", `<Native node={"type":"uiMetric","attrs":{"label":"Result"}} />`, `{"label":"Result"}`},

		{"native heading level", `<Native node={"type":"heading","attrs":{"level":9},"content":[{"type":"text","text":"Title"}]} />`, `9`},
		{"native image source", `<Native node={"type":"image","attrs":{"src":"javascript:alert(1)"}} />`, `"javascript:alert(1)"`},
		{"image alias source", `<Image src="javascript:alert(1)" alt="Image" />`, `"javascript:alert(1)"`},
		{"native mark link", `<Native node={"type":"paragraph","content":[{"type":"text","text":"é","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]} />`, `"javascript:alert(1)"`},
		{"native mark type", `<Native node={"type":"paragraph","content":[{"type":"text","text":"Text","marks":[{"type":"unknown"}]}]} />`, `"unknown"`},
		{"native alignment", `<Native node={"type":"paragraph","attrs":{"textAlign":"diagonal"}} />`, `"diagonal"`},
		{"native unknown attribute", `<Native node={"type":"paragraph","attrs":{"zUnknown":42,"aUnknown":123}} />`, `123`},
		{"native protected type", `<Native node={"type":"paragraph","attrs":{"agentProtected":"yes"}} />`, `"yes"`},
		{"native source version", `<Native node={"type":"paragraph","attrs":{"sourceVersionIds":["00000000-0000-0000-0000-000000000001","bad-id"]}} />`, `"bad-id"`},
		{"native captured evidence", `<Native node={"type":"taskCollection","attrs":{"capturedTasks":[]}} />`, `[]`},
		{"native table span", `<Native node={"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"rowspan":0},"content":[{"type":"paragraph"}]}]}]} />`, `0`},
		{"native table width", `<Native node={"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"colwidth":[100,-1]},"content":[{"type":"paragraph"}]}]}]} />`, `-1`},
		{"native task identity", `<Native node={"type":"taskReference","attrs":{"taskId":"not-a-task"}} />`, `"not-a-task"`},
		{"native workflow key", `<Native node={"type":"workflowReference","attrs":{"workflowKey":"Invalid Key"}} />`, `"Invalid Key"`},
		{"native display", `<Native node={"type":"outputCollection","attrs":{"display":"unknown"}} />`, `"unknown"`},
		{"native structure", `<Native node={"type":"bulletList","content":[{"type":"paragraph"}]} />`, `"paragraph"`},
		{"scope input", `<Scope name="sample" initial={"amount":{"invalid":true}}>`, `{"invalid":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileCatalogFixture(test.source, 1)
			var diagnostic *SourceDiagnostic
			if !errors.As(err, &diagnostic) {
				t.Fatalf("error = %v, want source diagnostic", err)
			}
			if diagnostic.Start < 0 || diagnostic.End > len(test.source) {
				t.Fatalf("invalid span %+v", diagnostic)
			}
			got := test.source[diagnostic.Start:diagnostic.End]
			if got != test.selected {
				t.Fatalf(
					"span = %q at %d:%d, want %q (%v)",
					got,
					diagnostic.Start,
					diagnostic.End,
					test.selected,
					err,
				)
			}
			if (test.name == "duplicate option" || test.name == "wrapped options") &&
				diagnostic.Start != strings.LastIndex(test.source, `"a"`) {
				t.Fatal("duplicate diagnostic must identify the second occurrence")
			}
		})
	}
}

func TestSourceJSONChildLocations(t *testing.T) {
	t.Parallel()
	raw := ` { "name": "日本語", "values": [ 1, {"x": "escaped \\\" value"} ], "name": "last" } `
	start, end, ok := sourceJSONChildSpan(raw, "name")
	if !ok || raw[start:end] != `"last"` {
		t.Fatalf("last duplicate value = %q, %v", raw[start:end], ok)
	}
	start, end, ok = sourceJSONChildSpan(`[ 1, {"x":2} ]`, "1")
	if !ok || start != 5 || end != 12 {
		t.Fatalf("array span = %d:%d, %v", start, end, ok)
	}
}

func TestSourceLexicalLocations(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, source, selected, code string }{
		{"state", `<Metric label="結果" value={"op":"state","name":"absent"} />`, `"absent"`, "state.undeclared"},
		{"data", `<Metric label="Result" value={"op":"data","name":"tasks"} />`, `"tasks"`, "data.undeclared"},
		{"item", `<Value value={"op":"item"} />`, `"item"`, "item.undeclared"},
		{"shorthand", `<Metric label="Result" value={state.absent + 1} />`, `state.absent`, "state.undeclared"},
		{"shorthand literal text", `<Metric label="Result" value={"state.absent" + state.absent} />`, `state.absent`, "state.undeclared"},
		{"shorthand repeated item", `<Metric label="Result" value={count(map([1], item)) + item} />`, `item`, "item.undeclared"},
		{"shorthand nested data", `<Metric label="Result" value={count(filter(data.tasks, item.done))} />`, `data.tasks`, "data.undeclared"},
		{"shorthand field access", `<Metric label="Result" value={item["title"]} />`, `item`, "item.undeclared"},
		{"shorthand computed", "<Scope name=\"sample\" initial={} computed={{\"total\":1 + state.absent}}>\nText\n</Scope>", `state.absent`, "state.undeclared"},
		{"shorthand action", "<Scope name=\"sample\" initial={\"amount\":1}>\n<Button label=\"Next\" action={{\"type\":\"setState\",\"name\":\"amount\",\"value\":state.amount + state.absent}} />\n</Scope>", `state.absent`, "state.undeclared"},
		{"shorthand action target", `<Button label="Next" action={{"type":"setState","name":"absent","value":1 + 2}} />`, `"absent"`, "binding.undeclared"},
		{"binding", `<TextInput label="Name" bind="absent" />`, `"absent"`, "binding.undeclared"},
		{"button target", `<Button label="Next" action={"type":"setState","name":"absent","value":{"op":"literal","value":1}} />`, `"absent"`, "binding.undeclared"},
		{"computed", "<Scope name=\"sample\" initial={} computed={\"total\":{\"op\":\"data\",\"name\":\"absent\"}}>\nText\n</Scope>", `"absent"`, "data.undeclared"},
		{"native descendant", `<Native node={"type":"paragraph","content":[{"type":"text","text":"é"},{"type":"uiValue","attrs":{"value":{"op":"state","name":"absent"}}}]} />`, `"absent"`, "state.undeclared"},
		{"wrapped native descendant", `<Native node={{"type":"paragraph","content":[{"type":"uiValue","attrs":{"value":{"op":"item"}}}]}} />`, `"item"`, "item.undeclared"},
		{"collection body", `<Metric label="Result" value={"op":"map","input":{"op":"literal","value":[1]},"body":{"op":"state","name":"absent"}} />`, `"absent"`, "state.undeclared"},
		{"task filter", `<TaskCollection filter={"op":"state","name":"absent"} display="table" />`, `"absent"`, "state.undeclared"},
		{"nested after prose", "# Heading\n\n<Scope name=\"sample\" initial={\"amount\":1}>\nParagraph\n<Stack>\n  <Metric label=\"結果\" value={\"op\":\"add\",\"args\":[{\"op\":\"state\",\"name\":\"amount\"},{\"op\":\"state\",\"name\":\"absent\"}]} />\n</Stack>\n</Scope>", `"absent"`, "state.undeclared"},
		{"button value", "<Scope name=\"sample\" initial={\"amount\":1}>\n<Button label=\"Next\" action={\"type\":\"setState\",\"name\":\"amount\",\"value\":{\"op\":\"state\",\"name\":\"absent\"}} />\n</Scope>", `"absent"`, "state.undeclared"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := compileCatalogFixture(test.source, 1)
			var diagnostic *SourceDiagnostic
			if !errors.As(err, &diagnostic) || diagnostic.Code != test.code {
				t.Fatalf("diagnostic = %v, want %s", err, test.code)
			}
			if diagnostic.Start < 0 || diagnostic.End < diagnostic.Start ||
				diagnostic.End > len(test.source) {
				t.Fatalf("invalid span %+v", diagnostic)
			}
			if got := test.source[diagnostic.Start:diagnostic.End]; got != test.selected {
				t.Fatalf("selected = %q, want %q (%v)", got, test.selected, diagnostic)
			}
			if (test.name == "shorthand repeated item" || test.name == "shorthand literal text") &&
				diagnostic.Start != strings.LastIndex(test.source, test.selected) {
				t.Fatal("diagnostic selected a preceding valid reference or literal")
			}
		})
	}
}

func TestSourceReplacementErrorStaysWithinTarget(t *testing.T) {
	t.Parallel()
	metric := BlockNode{Type: "uiMetric", Attrs: map[string]any{
		"blockId": "00000000-0000-0000-0000-000000000001", "label": "Result",
		"value": map[string]any{"op": "state", "name": "missing"},
	}}
	root := BlockNode{Type: "doc", Content: []BlockNode{metric, {
		Type: "paragraph", Attrs: map[string]any{"blockId": "00000000-0000-0000-0000-000000000002"},
	}}}
	err := ValidateUIDocument(root)
	if err == nil {
		t.Fatal("missing state was accepted")
	}
	source := BlocksSource(metric)
	id := metric.Attrs["blockId"].(string)
	for _, test := range []struct {
		name, source, id string
		wantLocated      bool
	}{
		{"target", source, id, true},
		{"another target", source, "00000000-0000-0000-0000-000000000002", false},
		{"malformed source", "<Native", id, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := LocateSourceReplacementError(test.source, root, test.id, err)
			if !test.wantLocated {
				if !errors.Is(got, err) {
					t.Fatalf("unrelated diagnostic = %v, want original %v", got, err)
				}
				return
			}
			var diagnostic *SourceDiagnostic
			if !errors.As(got, &diagnostic) || diagnostic.Code != "state.undeclared" ||
				diagnostic.BlockID != id {
				t.Fatalf("diagnostic = %v, want target state reference", got)
			}
			if selected := source[diagnostic.Start:diagnostic.End]; selected != `"missing"` {
				t.Fatalf("selected = %q, want missing reference", selected)
			}
		})
	}
}

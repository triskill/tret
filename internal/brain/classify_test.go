package brain

import "testing"

func TestClassifyToolCall(t *testing.T) {
	d := Classify(&Response{
		ToolCalls: []ToolCall{{ID: "1", Name: "bash", Arguments: `{"command":"ls -la"}`}},
	})
	if d.Route != "tool" || d.Command != "ls -la" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyRejectsMultipleToolCalls(t *testing.T) {
	d := Classify(&Response{
		ToolCalls: []ToolCall{
			{ID: "1", Name: "bash", Arguments: `{"command":"ls"}`},
			{ID: "2", Name: "bash", Arguments: `{"command":"rm -rf /"}`},
		},
	})
	if d.Route != "fail" || d.Command != "" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyBadToolArgumentsFails(t *testing.T) {
	d := Classify(&Response{
		ToolCalls: []ToolCall{{ID: "1", Name: "bash", Arguments: `not json`}},
	})
	if d.Route != "fail" || d.Reason == "" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyUnknownToolFails(t *testing.T) {
	d := Classify(&Response{
		ToolCalls: []ToolCall{{ID: "1", Name: "nukes", Arguments: `{"command":"ls"}`}},
	})
	if d.Route != "fail" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyFinishObject(t *testing.T) {
	d := Classify(&Response{Content: `{"route":"finish","message":"all done"}`})
	if d.Route != "finish" || d.Text != "all done" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyFailObject(t *testing.T) {
	d := Classify(&Response{Content: `{"route":"fail","error":"disk full"}`})
	if d.Route != "fail" || d.Reason != "disk full" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyPlainTextIsMessage(t *testing.T) {
	d := Classify(&Response{Content: "Here is what I found..."})
	if d.Route != "message" || d.Text != "Here is what I found..." {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyUnknownRouteObjectIsMessage(t *testing.T) {
	d := Classify(&Response{Content: `{"route":"teleport","message":"beam me up"}`})
	if d.Route != "message" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyEmptyResponseFails(t *testing.T) {
	d := Classify(&Response{})
	if d.Route != "fail" || d.Reason == "" {
		t.Fatalf("got %+v", d)
	}
}

func TestClassifyJSONInProseIsMessage(t *testing.T) {
	// prose containing braces must not be misread as a route object
	d := Classify(&Response{Content: `You can run {"route":"finish"} like this`})
	if d.Route != "message" {
		t.Fatalf("got %+v", d)
	}
}

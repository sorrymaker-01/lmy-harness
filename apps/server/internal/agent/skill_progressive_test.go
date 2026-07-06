package agent

import "testing"

func TestParseModelSkillRequestXML(t *testing.T) {
	request, ok := parseModelSkillRequest(`<load_skill name="earnings-analysis">Need the full workflow.</load_skill>`)
	if !ok {
		t.Fatal("expected request to parse")
	}
	if request.Name != "earnings-analysis" {
		t.Fatalf("unexpected skill name: %q", request.Name)
	}
	if request.Reason != "Need the full workflow." {
		t.Fatalf("unexpected reason: %q", request.Reason)
	}
}

func TestParseModelSkillRequestLine(t *testing.T) {
	request, ok := parseModelSkillRequest("LOAD_SKILL: /custom-review need repository rules")
	if !ok {
		t.Fatal("expected request to parse")
	}
	if request.Name != "custom-review" {
		t.Fatalf("unexpected skill name: %q", request.Name)
	}
	if request.Reason != "need repository rules" {
		t.Fatalf("unexpected reason: %q", request.Reason)
	}
}

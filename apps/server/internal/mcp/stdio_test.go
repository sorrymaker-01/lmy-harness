package mcp

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestReadRPCMessage(t *testing.T) {
	body := `{"id":1,"result":{"tools":[]}}`
	reader := bufio.NewReader(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)))
	response, err := readRPCMessage(reader)
	if err != nil {
		t.Fatalf("readRPCMessage returned error: %v", err)
	}
	if intID(response.ID) != 1 {
		t.Fatalf("unexpected id: %#v", response.ID)
	}
	if !strings.Contains(string(response.Result), "tools") {
		t.Fatalf("unexpected result: %s", response.Result)
	}
}

func TestMCPToolName(t *testing.T) {
	got := mcpToolName("file-system", "read.file")
	if got != "mcp__file_system__read_file" {
		t.Fatalf("unexpected tool name: %q", got)
	}
}

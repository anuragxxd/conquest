package filer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTerminationMessageStaysParseableUnderKubeletCap(t *testing.T) {
	// '<' is the worst case for JSON escaping: Go writes it as <.
	data := terminationMessage(0, "stage_out_error", strings.Repeat("<", 10000))
	if len(data) > 4096 {
		t.Fatalf("termination message is %d bytes, over the kubelet's 4096-byte cap", len(data))
	}
	var msg struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("termination message is not valid JSON: %v", err)
	}
	if msg.Status != "stage_out_error" || !strings.HasSuffix(msg.Error, "...") {
		t.Fatalf("got status %q, error ending %q", msg.Status, msg.Error[len(msg.Error)-5:])
	}
}

func TestTerminationMessageKeepsShortErrorsWhole(t *testing.T) {
	data := terminationMessage(0, "stage_out_error", "x509: certificate signed by unknown authority")
	want := `{"error":"x509: certificate signed by unknown authority","exit_code":0,"status":"stage_out_error"}`
	if string(data) != want {
		t.Fatalf("got %s, want %s", data, want)
	}
}

package opencode

import (
	"encoding/json"
	"testing"
)

func TestStripJSONCKeepsStrings(t *testing.T) {
	in := `{"permission": {"bash": {"echo a,}": "deny", "x // y": "ask",},}, /* c */ "k": [1, 2,],}`
	out, comments := stripJSONC([]byte(in))
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil || !comments {
		t.Fatalf("%v %s", err, out)
	}
	bash := v["permission"].(map[string]any)["bash"].(map[string]any)
	if bash["echo a,}"] != "deny" || bash["x // y"] != "ask" {
		t.Errorf("string contents changed: %s", out)
	}
}

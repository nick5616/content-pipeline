package cli

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Every API operation has a CLI twin that exists, so nothing is UI-only.
func TestEveryOperationHasACLITwin(t *testing.T) {
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	ops := 0
	for path, item := range spec.Paths {
		for method, node := range item {
			if method == "parameters" {
				continue
			}
			var op struct {
				ID  string `yaml:"operationId"`
				CLI string `yaml:"x-cli"`
			}
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}
			ops++
			where := strings.ToUpper(method) + " " + path
			switch {
			case op.ID == "":
				t.Errorf("%s: no operationId", where)
			case op.CLI == "":
				t.Errorf("%s (%s): no x-cli twin", where, op.ID)
			case op.CLI == "none":
				if !strings.HasPrefix(path, "/api/local/") {
					t.Errorf("%s (%s): only the read-only local cache may have no CLI twin", where, op.ID)
				}
			default:
				words := strings.Fields(op.CLI)
				if words[0] != "canon" || len(words) < 2 {
					t.Errorf("%s: x-cli %q must start with canon <command>", op.ID, op.CLI)
					continue
				}
				cmd, _, err := root.Find(words[1:])
				if err != nil || cmd == root || cmd.Name() != words[1] {
					t.Errorf("%s: x-cli %q names no canon command", op.ID, op.CLI)
				}
			}
		}
	}
	if ops < 20 {
		t.Fatalf("only %d operations found", ops)
	}
}

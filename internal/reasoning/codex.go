package reasoning

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

var codexProbes sync.Map

// CodexSupportsMax checks the installed runtime's exported ReasoningEffort
// schema. This avoids guessing a minimum version from conflicting/stale docs.
// The schema generator does not start a session, authenticate or call a model.
// Missing tools, old schema generators and all failures are conservative.
func CodexSupportsMax(parent context.Context) bool {
	path, err := exec.LookPath("codex")
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	cacheKey := path + ":" + info.ModTime().String()
	if value, ok := codexProbes.Load(cacheKey); ok {
		return value.(bool)
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "getbeeapi-codex-capability-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	command := codexSchemaCommand(ctx, path, dir)
	command.Dir = dir
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.WaitDelay = time.Second
	if command.Run() != nil {
		return false
	}
	file, err := os.Open(filepath.Join(dir, "codex_app_server_protocol.schemas.json"))
	if err != nil {
		return false
	}
	defer file.Close()
	var schema any
	if json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&schema) != nil {
		return false
	}
	allowed := schemaAllowsMax(schema)
	codexProbes.Store(cacheKey, allowed)
	return allowed
}

func schemaAllowsMax(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if definition, ok := object["ReasoningEffort"].(map[string]any); ok {
		if values, enumerated := definition["enum"].([]any); enumerated {
			for _, value := range values {
				if value == "max" {
					return true
				}
			}
			return false
		}
		// Recent Codex runtimes use a model-advertised, non-empty string instead
		// of the legacy fixed enum. Do not treat arbitrary schema strings as proof.
		return definition["type"] == "string" && definition["minLength"] == float64(1) && definition["pattern"] == nil
	}
	for key, nested := range object {
		if key == "definitions" || key == "$defs" || key == "v2" {
			if schemaAllowsMax(nested) {
				return true
			}
		}
	}
	return false
}

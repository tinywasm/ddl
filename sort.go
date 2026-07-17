package ddl

import (
	"github.com/tinywasm/fmt"
	"github.com/tinywasm/model"
)

// TopologicalSort returns models sorted so parents come before children (Kahn's BFS).
// Models not implementing SchemaExt() are treated as having no FK deps.
// Returns error on circular FK dependency. Moved here from tinywasm/ddlc: ordering
// CREATE TABLE statements by FK dependency is a DDL concern, not a generation-tool concern
// — see DDLC_DEPENDENCY_PROPOSAL.md in tinywasm/app-releases/docs.
func TopologicalSort(models []model.Model) ([]model.Model, error) {
	byName := make(map[string]model.Model, len(models))
	rdeps := make(map[string][]string)
	inDeg := make(map[string]int, len(models))

	for _, m := range models {
		name := m.ModelName()
		byName[name] = m
		if ext, ok := m.(interface{ SchemaExt() []model.FieldExt }); ok {
			for _, f := range ext.SchemaExt() {
				if f.Ref != "" {
					rdeps[f.Ref] = append(rdeps[f.Ref], name)
					inDeg[name]++
				}
			}
		}
	}

	var queue []string
	for _, m := range models {
		if inDeg[m.ModelName()] == 0 {
			queue = append(queue, m.ModelName())
		}
	}

	result := make([]model.Model, 0, len(models))
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		result = append(result, byName[name])
		for _, dep := range rdeps[name] {
			inDeg[dep]--
			if inDeg[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(result) != len(models) {
		return nil, fmt.Err("ddl: circular FK dependency detected")
	}
	return result, nil
}

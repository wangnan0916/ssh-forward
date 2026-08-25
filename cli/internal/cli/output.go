package cli

import "encoding/json"

func (a *App) writeJSON(value any) error {
	return json.NewEncoder(a.Options.Stdout).Encode(value)
}

func mutationJSONKey(adding bool) string {
	if adding {
		return "added"
	}
	return "removed"
}

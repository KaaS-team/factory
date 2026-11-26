package mcp

import "errors"

// PrioritizeInput is the input of prioritize.
type PrioritizeInput struct {
	Method  string `json:"method,omitempty" jsonschema:"rice or wsjf; default: the prioritize.method setting"`
	Approve bool   `json:"approve,omitempty" jsonschema:"store the ranks, which pickup follows"`
}

func init() {
	registerTool("prioritize", true, backlogAdd("prioritize", "Rank the ready issues by RICE or WSJF; with approve, store the ranks pickup follows",
		func(in PrioritizeInput) ([]string, string, error) {
			a := []string{"prioritize"}
			if in.Method != "" {
				if in.Method != "rice" && in.Method != "wsjf" {
					return nil, "", errors.New("prioritize: method must be rice or wsjf")
				}
				a = append(a, "--method", in.Method)
			}
			if in.Approve {
				a = append(a, "--approve")
			}
			return a, "", nil
		}))
}

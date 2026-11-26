package mcp

import "errors"

// DecomposeInput is the input of decompose.
type DecomposeInput struct {
	Epic    string `json:"epic" jsonschema:"epic issue number"`
	Text    string `json:"text,omitempty" jsonschema:"child issue blocks; default: the blocks in the epic body"`
	Approve bool   `json:"approve,omitempty" jsonschema:"file the children and link them as sub-issues"`
}

func init() {
	registerTool("decompose", true, backlogAdd("decompose", "Split an epic into child issues linked as sub-issues",
		func(in DecomposeInput) ([]string, string, error) {
			if !digits(in.Epic) {
				return nil, "", errors.New("decompose: epic must be an issue number")
			}
			a := []string{"decompose", in.Epic}
			if in.Text != "" {
				a = append(a, "--from", "-")
			}
			if in.Approve {
				a = append(a, "--approve")
			}
			return a, in.Text, nil
		}))
}

package mcp

// SpecInput is the input of spec: issue blocks to validate and, with Approve, file.
type SpecInput struct {
	Text    string `json:"text" jsonschema:"issue blocks: '--- issue: <title>' then Problem, Acceptance and Out of scope sections"`
	Approve bool   `json:"approve,omitempty" jsonschema:"file the issues; without it nothing is written"`
}

func init() {
	registerTool("spec", true, backlogAdd("spec", "Check issue drafts against the Problem, Acceptance and Out of scope form and, with approve, file them",
		func(in SpecInput) ([]string, string, error) {
			a := []string{"spec", "--from", "-"}
			if in.Approve {
				a = append(a, "--approve")
			}
			return a, in.Text, nil
		}))
}

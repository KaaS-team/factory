package web

import (
	"fmt"
	"html/template"

	ui "github.com/KaaS-team/ui"
)

// funcs build ui partial data inside page templates.
var funcs = template.FuncMap{
	"head": func(title string) ui.Head {
		return ui.Head{Title: title + " · Factory", Description: "The factory fleet: runs, attention and live timelines.", Styles: []string{"/static/app.css"}}
	},
	"button": func(label, typ, variant string) ui.Button {
		return ui.Button{Label: label, Type: typ, Variant: variant}
	},
	"empty": func(title, body string) ui.Empty { return ui.Empty{Title: title, Body: body} },
	"stat":  func(label string, v any) ui.Stat { return ui.Stat{Label: label, Value: fmt.Sprint(v)} },
	"usd":   func(v float64) string { return fmt.Sprintf("$%.2f", v) },
}

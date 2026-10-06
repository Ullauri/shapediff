package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/forge"
	"github.com/Ullauri/shapediff/internal/graph"
	"github.com/Ullauri/shapediff/internal/narrate"
	"github.com/Ullauri/shapediff/internal/render"
)

func init() {
	register(command{name: "render", synopsis: "--delta delta.json --head head.json --base base.json [--project-url URL] [--forge gitlab|github] [--diagram-only] [-o note.md]", short: "render a delta as an MR note (Mermaid + narration)", run: runRender})
}

// renderNote is the shared path from delta and graphs to note text. It is
// used by render and by post. It returns the note, the filter result (for
// the SVG fallback) and warnings to print.
func renderNote(d *delta.Delta, head, base *graph.Index, cfg *config.Config, projectURL string, fg forge.Forge, started time.Time) (string, *filter.Result, []string, error) {
	var warnings []string
	budget := filter.Budget{Chars: cfg.Budget.Chars, Nodes: cfg.Budget.Nodes, Edges: cfg.Budget.Edges, NarrowTargets: cfg.NarrowTargets}
	opts := filter.Options{Budget: budget, Measure: func(dg *filter.Diagram) int { return len(render.Mermaid(dg)) }}
	res := filter.Apply(d, head, base, opts)

	// The summary list comes from the template even with narration off:
	// its counts are the headline. Off drops the names under each count.
	sentences := narrate.Template(d, head, base)
	if cfg.Narration != "off" {
		if cfg.Narration == "llm" {
			client, why := llmClient(os.Getenv)
			if client == nil {
				warnings = append(warnings, why)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				rewritten, err := narrate.Rewrite(ctx, client, sentences, d, head, base)
				cancel()
				if err != nil {
					warnings = append(warnings, "narration: llm rewrite failed, using the template: "+err.Error())
				}
				sentences = rewritten
			}
		}
	}
	var summary []render.SummaryItem
	for _, s := range sentences {
		switch {
		case s.Rewritten:
			summary = append(summary, render.SummaryItem{Text: s.Text})
		case s.Lead == "":
			// said elsewhere on the page (see narrate.Sentence)
		case cfg.Narration == "off":
			summary = append(summary, render.SummaryItem{Text: s.Lead})
		default:
			summary = append(summary, render.SummaryItem{Text: s.Lead, Items: s.Items, IDs: s.ItemIDs})
		}
	}
	var bl *filter.Baseline
	if cfg.Baseline != "off" {
		bl = filter.BaselineView(d, head, base, opts)
	}
	note, err := render.Note(render.NoteInput{
		Delta: d, Head: head, Base: base, Filter: res, Baseline: bl,
		Summary: summary, Unchanged: narrate.Unchanged(d), Suppression: narrate.SuppressionLine(d),
		ProjectURL: projectURL, Forge: fg, WeeklyURL: cfg.WeeklyURL, HeadSHA: d.HeadSHA, BaseSHA: d.BaseSHA, MRIID: d.MRIID,
		Extractor: head.Doc.Extractor, Elapsed: time.Since(started),
	})
	return note, res, warnings, err
}

func runRender(args []string) error {
	fs := newFlags("render")
	deltaPath := fs.String("delta", "", "delta document (required)")
	headPath := fs.String("head", "", "head graph (required)")
	basePath := fs.String("base", "", "base graph (required)")
	cfgPath := fs.String("config", config.FileName, "repo config; defaults when the file is absent")
	projectURL := fs.String("project-url", "", "project web URL for permalinks")
	forgeName := fs.String("forge", "", "gitlab or github: the permalink shape and what the note calls the change (default: from --project-url, github.com is github)")
	weeklyURL := fs.String("weekly-url", "", "URL of the weekly structure brief; overrides weekly_url in the config")
	mrIID := fs.Int("mr-iid", 0, "merge request iid to record in the note")
	out := fs.String("o", "", "output file (default stdout)")
	diagramOnly := fs.Bool("diagram-only", false, "print only the primary call-path Mermaid source")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *deltaPath == "" || *headPath == "" || *basePath == "" {
		return usagef("--delta, --head and --base are required")
	}
	fg, err := forgeFor(*forgeName, *projectURL)
	if err != nil {
		return usagef("%v", err)
	}
	started := time.Now()
	d, err := delta.ReadFile(*deltaPath)
	if err != nil {
		return err
	}
	if *mrIID != 0 {
		d.MRIID = *mrIID
	}
	headDoc, err := graph.ReadFile(*headPath)
	if err != nil {
		return err
	}
	baseDoc, err := graph.ReadFile(*basePath)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *weeklyURL != "" {
		cfg.WeeklyURL = *weeklyURL
	}
	head, base := graph.NewIndex(headDoc), graph.NewIndex(baseDoc)

	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if *diagramOnly {
		budget := filter.Budget{Chars: cfg.Budget.Chars, Nodes: cfg.Budget.Nodes, Edges: cfg.Budget.Edges, NarrowTargets: cfg.NarrowTargets}
		res := filter.Apply(d, head, base, filter.Options{Budget: budget, Measure: func(dg *filter.Diagram) int { return len(render.Mermaid(dg)) }})
		if len(res.CallPaths) == 0 {
			return fmt.Errorf("no call-path diagram")
		}
		_, err := io.WriteString(w, render.Mermaid(&res.CallPaths[0]))
		return err
	}
	note, res, warnings, err := renderNote(d, head, base, cfg, *projectURL, fg, started)
	if err != nil {
		return err
	}
	for _, wmsg := range warnings {
		fmt.Fprintln(os.Stderr, "shapediff render:", wmsg)
	}
	if res.Fallback {
		fmt.Fprintln(os.Stderr, "shapediff render: the call-path diagram is over budget.chars, so the note lists it instead of drawing it; --diagram-only prints its Mermaid source")
	}
	_, err = io.WriteString(w, note)
	return err
}

// llmClient builds the narration rewrite client (spec 11) from the CI
// environment, and only from there. The repo config travels with the change
// under review, so letting it name the endpoint, or the variable holding the
// key, would let any branch send a CI secret to a server of its choosing.
// The endpoint must be https, or http on the CI host itself (a local model
// is the spec's own example). Nil and a reason means "use the template".
func llmClient(get func(string) string) (narrate.Client, string) {
	endpoint := strings.TrimSpace(get("SHAPEDIFF_LLM_ENDPOINT"))
	if endpoint == "" {
		return nil, "narration: llm requested but SHAPEDIFF_LLM_ENDPOINT is not set in the environment; using the template"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return nil, fmt.Sprintf("narration: SHAPEDIFF_LLM_ENDPOINT must be an https URL, or http on localhost, got %q; using the template", endpoint)
	}
	return narrate.OpenAIClient{Endpoint: endpoint, Model: get("SHAPEDIFF_LLM_MODEL"), APIKey: get("SHAPEDIFF_LLM_KEY")}, ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// forgeFor is the --forge flag, or the forge the project URL is on.
func forgeFor(flag, projectURL string) (forge.Forge, error) {
	if flag != "" {
		return forge.Parse(flag)
	}
	return forge.FromURL(projectURL), nil
}

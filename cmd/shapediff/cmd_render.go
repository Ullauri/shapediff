package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Ullauri/shapediff/internal/config"
	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/filter"
	"github.com/Ullauri/shapediff/internal/graph"
	"github.com/Ullauri/shapediff/internal/narrate"
	"github.com/Ullauri/shapediff/internal/render"
)

func init() {
	register(command{name: "render", short: "render a delta as an MR note (Mermaid + narration)", run: runRender})
}

// renderNote is the shared path from delta and graphs to note text. It is
// used by render and by post. It returns the note, the filter result (for
// the SVG fallback) and warnings to print.
func renderNote(d *delta.Delta, head, base *graph.Index, cfg *config.Config, projectURL string, started time.Time) (string, *filter.Result, []string, error) {
	var warnings []string
	budget := filter.Budget{Chars: cfg.Budget.Chars, Nodes: cfg.Budget.Nodes, Edges: cfg.Budget.Edges, NarrowTargets: cfg.NarrowTargets}
	opts := filter.Options{Budget: budget, Measure: func(dg *filter.Diagram) int { return len(render.Mermaid(dg)) }}
	res := filter.Apply(d, head, base, opts)

	var texts []string
	if cfg.Narration != "off" {
		sentences := narrate.Template(d, head, base)
		if cfg.Narration == "llm" {
			if cfg.LLM.Endpoint == "" {
				warnings = append(warnings, "narration: llm requested but no llm.endpoint configured; using the template")
			} else {
				client := narrate.OpenAIClient{Endpoint: cfg.LLM.Endpoint, Model: cfg.LLM.Model, APIKey: os.Getenv(cfg.LLM.APIKeyEnv)}
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				rewritten, err := narrate.Rewrite(ctx, client, sentences, d, head, base)
				cancel()
				if err != nil {
					warnings = append(warnings, "narration: llm rewrite failed, using the template: "+err.Error())
				}
				sentences = rewritten
			}
		}
		for _, s := range sentences {
			texts = append(texts, s.Text)
		}
	}
	var bl *filter.Baseline
	if cfg.Baseline != "off" {
		bl = filter.BaselineView(d, head, base, opts)
	}
	note, err := render.Note(render.NoteInput{
		Delta: d, Head: head, Base: base, Filter: res, Baseline: bl,
		Headline: narrate.Headline(d), Suppression: narrate.SuppressionLine(d), Sentences: texts,
		ProjectURL: projectURL, HeadSHA: d.HeadSHA, BaseSHA: d.BaseSHA, MRIID: d.MRIID,
		Extractor: head.Doc.Extractor, Elapsed: time.Since(started),
	})
	return note, res, warnings, err
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	deltaPath := fs.String("delta", "", "delta document (required)")
	headPath := fs.String("head", "", "head graph (required)")
	basePath := fs.String("base", "", "base graph (required)")
	cfgPath := fs.String("config", "", "repo config (.shapediff.yml); defaults when absent")
	projectURL := fs.String("project-url", "", "project web URL for permalinks")
	mrIID := fs.Int("mr-iid", 0, "merge request iid to record in the note")
	out := fs.String("o", "", "output file (default stdout)")
	diagramOnly := fs.Bool("diagram-only", false, "print only the primary call-path Mermaid source")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *deltaPath == "" || *headPath == "" || *basePath == "" {
		return fmt.Errorf("--delta, --head and --base are required")
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
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
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
	note, res, warnings, err := renderNote(d, head, base, cfg, *projectURL, started)
	if err != nil {
		return err
	}
	for _, wmsg := range warnings {
		fmt.Fprintln(os.Stderr, "shapediff render:", wmsg)
	}
	if res.Fallback {
		fmt.Fprintln(os.Stderr, "shapediff render: primary diagram over budget; render the SVG fallback with graphviz (see render.SVG)")
	}
	_, err = io.WriteString(w, note)
	return err
}

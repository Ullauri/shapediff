package narrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Ullauri/shapediff/internal/delta"
	"github.com/Ullauri/shapediff/internal/graph"
)

// Client produces JSON claims from a prompt. The only implementation here
// is OpenAIClient; tests use a fake.
type Client interface {
	Claims(ctx context.Context, system, user string) ([]Claim, error)
}

// Rewrite asks the client for fluent replacements and accepts each template
// sentence's replacement only when every claim made for it validates. A
// rejected or missing replacement keeps the template sentence, so the
// result has exactly as many sentences as the template, at most eight.
// A client error keeps the whole template; the error is returned so the
// caller can warn.
func Rewrite(ctx context.Context, client Client, sentences []Sentence, d *delta.Delta, head, base *graph.Index) ([]Sentence, error) {
	out := append([]Sentence(nil), sentences...)
	if len(out) > MaxSentences {
		out = out[:MaxSentences]
	}
	if client == nil || len(out) == 0 {
		return out, nil
	}
	user, err := prompt(out, d)
	if err != nil {
		return out, err
	}
	claims, err := client.Claims(ctx, systemPrompt, user)
	if err != nil {
		return out, err
	}
	byIndex := map[int][]Claim{}
	for _, c := range claims {
		byIndex[c.Replaces] = append(byIndex[c.Replaces], c)
	}
	idx := make([]int, 0, len(byIndex))
	for i := range byIndex {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		if i < 0 || i >= len(out) {
			continue
		}
		cs := byIndex[i]
		if len(ValidateClaims(cs, d, head, base)) > 0 {
			continue
		}
		// The rewrite must cover every claim the template made, or it is
		// saying less than the template and is dropped.
		if !covers(cs, out[i].Claims) {
			continue
		}
		var texts []string
		seen := map[string]bool{}
		for _, c := range cs {
			t := strings.TrimSpace(c.Sentence)
			if t != "" && !seen[t] {
				seen[t] = true
				texts = append(texts, t)
			}
		}
		if len(texts) == 0 {
			continue
		}
		out[i] = Sentence{Text: strings.Join(texts, " "), Claims: cs, Rewritten: true}
	}
	return out, nil
}

func covers(got, want []Claim) bool {
	have := map[[3]string]bool{}
	for _, c := range got {
		have[[3]string{c.SubjectID, c.Predicate, c.ObjectID}] = true
	}
	for _, c := range want {
		if !have[[3]string{c.SubjectID, c.Predicate, c.ObjectID}] {
			return false
		}
	}
	return true
}

const systemPrompt = `You rewrite templated sentences about a code change into fluent prose. You never see the diff; you see the structured delta and the template sentences. Reply with a JSON object {"claims":[...]} where each claim is {"subject_id","predicate","object_id","sentence","replaces"}: subject_id and object_id are node IDs from the delta, predicate is one of reaches, no_longer_reaches, calls, no_longer_calls, renamed_to, signature_changed, imports, no_longer_imports, added, removed, body_changed, suppressed, sentence is the fluent sentence that states exactly that fact and names both nodes by their short names, and replaces is the index of the template sentence it replaces. Every fact in a template sentence must be restated by a claim. State facts only; no judgement, advice or risk language.`

func prompt(sentences []Sentence, d *delta.Delta) (string, error) {
	var b strings.Builder
	b.WriteString("Template sentences:\n")
	for i, s := range sentences {
		fmt.Fprintf(&b, "%d. %s\n", i, s.Text)
		for _, c := range s.Claims {
			fmt.Fprintf(&b, "   claim: %s %s %s\n", c.SubjectID, c.Predicate, c.ObjectID)
		}
	}
	b.WriteString("\nDelta:\n")
	slim := struct {
		Nodes       delta.NodeDelta    `json:"nodes"`
		Entrypoints []delta.Entrypoint `json:"entrypoints"`
		Packages    delta.PackageDelta `json:"packages"`
		Suppressed  []delta.Suppressed `json:"suppressed"`
	}{d.Nodes, d.Entrypoints, d.Packages, d.Suppressed}
	js, err := json.Marshal(slim)
	if err != nil {
		return "", err
	}
	b.Write(js)
	return b.String(), nil
}

// OpenAIClient talks to any OpenAI-compatible chat completions endpoint.
type OpenAIClient struct {
	Endpoint string // full URL, e.g. http://localhost:8080/v1/chat/completions
	Model    string
	APIKey   string
	HTTP     *http.Client
}

// Claims implements Client.
func (c OpenAIClient) Claims(ctx context.Context, system, user string) ([]Claim, error) {
	if c.Endpoint == "" {
		return nil, fmt.Errorf("no llm endpoint configured")
	}
	body, _ := json.Marshal(map[string]any{
		"model":           c.Model,
		"messages":        []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llm endpoint returned %s", resp.Status)
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &cr); err != nil || len(cr.Choices) == 0 {
		return nil, fmt.Errorf("llm response not understood")
	}
	var payload struct {
		Claims []Claim `json:"claims"`
	}
	if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), &payload); err != nil {
		return nil, fmt.Errorf("llm content is not a claims object: %w", err)
	}
	return payload.Claims, nil
}

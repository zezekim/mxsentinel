package nlquery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zezekim/mxsentinel/internal/ai"
)

// PlannedQuery is one whitelisted tool call the model proposed.
type PlannedQuery struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// plan is the JSON envelope the model returns in the planning step.
type plan struct {
	Queries []PlannedQuery `json:"queries"`
}

// AskResult is the full response for one question.
type AskResult struct {
	Answer      string        `json:"answer"`
	UsedQueries []string      `json:"used_queries"`
	Data        []QueryResult `json:"data"`
}

const planSystemPrompt = `You are MX Sentinel's mail-analytics query planner.
You help mail operators by answering questions about their email infrastructure USING ONLY a fixed set of aggregate analytics tools.
You CANNOT read message bodies, subject lines, headers, or any raw mail content — that data is never available to you. Never ask for it and never claim to have read it.
You do NOT write SQL. You may only choose from the whitelisted tools below and supply their documented arguments.

Given the user's question, decide which tool(s) best answer it and with what arguments.
Respond with ONLY a single JSON object, no prose, in exactly this shape:
{"queries": [{"tool": "<tool_name>", "args": {"<param>": <value>}}]}
Pick the fewest tools that answer the question (usually one). Use the documented argument names only. Omit arguments you don't need.

Available tools:
`

const answerSystemPrompt = `You are MX Sentinel's mail-analytics assistant, helping a mail operator.
You are given the operator's question and the AGGREGATE results of whitelisted analytics queries (counts and rates only — never message bodies or subjects, which you cannot access).
Write a concise, direct, operational answer grounded ONLY in the provided aggregate data. Cite the concrete numbers. If the data does not answer the question, say so plainly. Do not invent facts, and do not claim to have inspected message contents.`

// BuildPlanPrompt renders the whitelisted tool catalog + the user's question into the
// planning user message. Pure.
func BuildPlanPrompt(question string) (system, user string) {
	var sb strings.Builder
	sb.WriteString(planSystemPrompt)
	for _, t := range Registry {
		fmt.Fprintf(&sb, "\n- %s: %s\n", t.Name, t.Description)
		for _, p := range t.Params {
			req := ""
			if p.Required {
				req = " (required)"
			}
			spec := string(p.Kind)
			if p.Kind == kindEnum {
				spec = "one of [" + strings.Join(p.Allowed, ", ") + "]"
			}
			fmt.Fprintf(&sb, "    - %s: %s%s — %s\n", p.Name, spec, req, p.Description)
		}
	}
	return sb.String(), "Question: " + strings.TrimSpace(question)
}

// parsePlan extracts the JSON plan object from a (possibly fenced/prose-wrapped) response.
// Pure.
func parsePlan(raw string) (plan, error) {
	jsonText := extractJSONObject(raw)
	if jsonText == "" {
		return plan{}, fmt.Errorf("no JSON object found in planner response")
	}
	var p plan
	if err := json.Unmarshal([]byte(jsonText), &p); err != nil {
		return plan{}, fmt.Errorf("parse plan json: %w", err)
	}
	return p, nil
}

// ValidatePlan checks each proposed query against the whitelist and validates its args,
// capping the number of queries at maxTools. It returns the normalized, executable plan.
// Pure — this is where an off-whitelist tool or a forbidden argument is rejected.
func ValidatePlan(p plan, maxTools int) ([]PlannedQuery, error) {
	if len(p.Queries) == 0 {
		return nil, fmt.Errorf("planner returned no queries")
	}
	if maxTools <= 0 {
		maxTools = 3
	}
	var out []PlannedQuery
	for _, q := range p.Queries {
		if len(out) >= maxTools {
			break
		}
		tool, ok := ToolByName(q.Tool)
		if !ok {
			return nil, fmt.Errorf("tool %q is not in the whitelist", q.Tool)
		}
		norm, err := ValidateArgs(tool, q.Args)
		if err != nil {
			return nil, err
		}
		out = append(out, PlannedQuery{Tool: q.Tool, Args: norm})
	}
	return out, nil
}

// RunPlan executes each validated query and returns their aggregate results. A per-tool
// error is surfaced (the whole request fails) rather than silently dropped, so callers
// never compose an answer over partial/misleading data without knowing.
func RunPlan(ctx context.Context, ex Executor, tenantID string, queries []PlannedQuery) ([]QueryResult, error) {
	results := make([]QueryResult, 0, len(queries))
	for _, q := range queries {
		tool, ok := ToolByName(q.Tool)
		if !ok {
			return nil, fmt.Errorf("tool %q is not in the whitelist", q.Tool)
		}
		res, err := tool.run(ctx, ex, tenantID, q.Args)
		if err != nil {
			return nil, fmt.Errorf("run %s: %w", q.Tool, err)
		}
		results = append(results, res)
	}
	return results, nil
}

// BuildAnswerPrompt renders the question + aggregate results into the answer-composition
// user message. Pure.
func BuildAnswerPrompt(question string, results []QueryResult) string {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		data = []byte("[]")
	}
	return "Operator question:\n" + strings.TrimSpace(question) +
		"\n\nAggregate query results (JSON):\n" + string(data) +
		"\n\nAnswer the question using only these aggregate results."
}

// Answer runs the full two-step flow: plan → validate → execute → compose. The LLM is
// reached only through the ai.Client interface, so tests inject a mock. tenantID scopes
// every executed query.
func Answer(ctx context.Context, client ai.Client, ex Executor, cfg Config, tenantID, question string) (AskResult, error) {
	if strings.TrimSpace(question) == "" {
		return AskResult{}, fmt.Errorf("question is empty")
	}

	planSys, planUser := BuildPlanPrompt(question)
	planRaw, err := client.Complete(ctx, planSys, planUser)
	if err != nil {
		return AskResult{}, fmt.Errorf("planner llm: %w", err)
	}
	parsed, err := parsePlan(planRaw)
	if err != nil {
		return AskResult{}, err
	}
	queries, err := ValidatePlan(parsed, cfg.MaxTools)
	if err != nil {
		return AskResult{}, err
	}

	results, err := RunPlan(ctx, ex, tenantID, queries)
	if err != nil {
		return AskResult{}, err
	}

	answer, err := client.Complete(ctx, answerSystemPrompt, BuildAnswerPrompt(question, results))
	if err != nil {
		return AskResult{}, fmt.Errorf("answer llm: %w", err)
	}

	used := make([]string, 0, len(queries))
	for _, q := range queries {
		used = append(used, q.Tool)
	}
	return AskResult{
		Answer:      strings.TrimSpace(answer),
		UsedQueries: used,
		Data:        results,
	}, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}', stripping any
// markdown code fences first. Returns "" if no braces are present. (Mirrors the helper in
// internal/ai, kept local so nlquery has no dependency on ai's internals.)
func extractJSONObject(s string) string {
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

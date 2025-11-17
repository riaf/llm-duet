package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		if err := fs.Parse(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := runInit(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "plan":
		fs := flag.NewFlagSet("plan", flag.ExitOnError)
		idea := fs.String("idea", "", "idea text")
		ideaFile := fs.String("idea-file", "", "idea file path")
		hint := fs.String("hint", "", "hint text")
		hintFile := fs.String("hint-file", "", "hint file path")
		if err := fs.Parse(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := runPlan(*idea, *ideaFile, *hint, *hintFile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: llm-duet <command> [options]")
	fmt.Println("Commands:")
	fmt.Println("  init")
	fmt.Println("  plan")
}

func runInit() error {
	base := ".llm-duet"
	workspace := filepath.Join(base, "workspace")
	prompts := filepath.Join(base, "prompts")
	config := filepath.Join(base, "config")

	dirs := []string{base, workspace, prompts, config}
	for _, d := range enumerateUnique(dirs) {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}

	if err := writeIfNotExists(filepath.Join(prompts, "planner.txt"), defaultPlannerPrompt); err != nil {
		return err
	}
	if err := writeIfNotExists(filepath.Join(prompts, "reviewer.txt"), defaultReviewerPrompt); err != nil {
		return err
	}
	gitignorePath := filepath.Join(base, ".gitignore")
	if err := writeIfNotExists(gitignorePath, "workspace/\n"); err != nil {
		return err
	}

	return nil
}

func runPlan(idea, ideaFile, hint, hintFile string) error {
	base := ".llm-duet"
	workspace := filepath.Join(base, "workspace")
	if _, err := os.Stat(base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New(".llm-duet/ が見つかりません。先に `llm-duet init` を実行してください。")
		}
		return err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}

	if err := checkCLIs(); err != nil {
		return err
	}

	lockPath := filepath.Join(workspace, "lock")
	if _, err := os.Stat(lockPath); err == nil {
		return errors.New("別の llm-duet plan が実行中、または前回の実行が異常終了した可能性があります。\n.llm-duet/workspace/ の内容を確認し、必要に応じて .llm-duet/workspace/lock を手動で削除してから再実行してください。")
	}

	lockFile, err := os.Create(lockPath)
	if err != nil {
		return fmt.Errorf("failed to create lock: %w", err)
	}
	lockFile.Close()

	success := false
	defer func() {
		if success {
			_ = os.Remove(lockPath)
		}
	}()

	planPath := "plan.md"
	planExists := fileExists(planPath)

	if planExists && (idea != "" || ideaFile != "") {
		return errors.New("plan.md がすでに存在するため、--idea / --idea-file は使用できません。最初から作り直したい場合は plan.md を削除してから再実行してください。")
	}

	if !planExists && idea == "" && ideaFile == "" {
		return errors.New("初回実行では --idea または --idea-file の指定が必要です。例: llm-duet plan --idea \"作りたいもの\"")
	}

	ideaText := idea
	if ideaText == "" {
		var err error
		ideaText, err = readOptionalFile(ideaFile)
		if err != nil {
			return err
		}
	}

	hintText, err := mergeHint(hint, hintFile)
	if err != nil {
		return err
	}

	if planExists {
		if err := runPlanUpdate(planPath, workspace, hintText); err != nil {
			return err
		}
	} else {
		if err := runPlanInitial(planPath, workspace, ideaText, hintText); err != nil {
			return err
		}
	}

	success = true
	return nil
}

func runPlanInitial(planPath, workspace, ideaText, hintText string) error {
	plannerInput := filepath.Join(workspace, "planner_input.txt")
	draftPath := filepath.Join(workspace, "plan_draft.md")
	reviewDraftPath := filepath.Join(workspace, "review_draft.json")
	generatedPath := filepath.Join(workspace, "plan_generated.md")

	if err := writePlannerInput(plannerInput, ideaText, hintText, "(none)", "{}"); err != nil {
		return err
	}

	if err := runClaude(plannerInput, draftPath); err != nil {
		return fmt.Errorf("Claude 実行中に失敗しました (.llm-duet/workspace/plan_draft.md を確認してください): %w", err)
	}

	reviewerInput := filepath.Join(workspace, "reviewer_input.txt")
	if err := writeReviewerInput(reviewerInput, filepath.Join(".llm-duet", "prompts", "reviewer.txt"), draftPath); err != nil {
		return err
	}

	if err := runCodex(reviewerInput, reviewDraftPath); err != nil {
		return fmt.Errorf("Codex レビュー実行中に失敗しました (.llm-duet/workspace/review_draft.json を確認してください): %w", err)
	}

	reviewContent, err := os.ReadFile(reviewDraftPath)
	if err != nil {
		return err
	}
	if err := validateReviewJSON(reviewContent); err != nil {
		return fmt.Errorf("Codex が出力した JSON が不正です。.llm-duet/workspace/review_draft.json を確認してください: %w", err)
	}

	draftContent, err := os.ReadFile(draftPath)
	if err != nil {
		return err
	}

	if err := writePlannerInput(plannerInput, ideaText, hintText, string(draftContent), string(reviewContent)); err != nil {
		return err
	}

	if err := runClaude(plannerInput, generatedPath); err != nil {
		return fmt.Errorf("Claude 実行中に失敗しました (.llm-duet/workspace/plan_generated.md を確認してください): %w", err)
	}

	if err := copyFile(generatedPath, planPath); err != nil {
		return err
	}

	return nil
}

func runPlanUpdate(planPath, workspace, hintText string) error {
	plannerInput := filepath.Join(workspace, "planner_input.txt")
	generatedPath := filepath.Join(workspace, "plan_generated.md")
	reviewPath := filepath.Join(workspace, "review.json")
	reviewerInput := filepath.Join(workspace, "reviewer_input.txt")
	prevPath := filepath.Join(workspace, "plan_prev.md")

	if err := copyFile(planPath, prevPath); err != nil {
		return err
	}

	if err := writeReviewerInput(reviewerInput, filepath.Join(".llm-duet", "prompts", "reviewer.txt"), prevPath); err != nil {
		return err
	}

	if err := runCodex(reviewerInput, reviewPath); err != nil {
		return fmt.Errorf("Codex レビュー実行中に失敗しました (.llm-duet/workspace/review.json を確認してください): %w", err)
	}

	reviewContent, err := os.ReadFile(reviewPath)
	if err != nil {
		return err
	}
	if err := validateReviewJSON(reviewContent); err != nil {
		return fmt.Errorf("Codex が出力した JSON が不正です。.llm-duet/workspace/review.json を確認してください: %w", err)
	}

	prevContent, err := os.ReadFile(prevPath)
	if err != nil {
		return err
	}

	hintSection := hintText
	if hintSection == "" {
		hintSection = "(none)"
	}

	if err := writePlannerInput(plannerInput, "(none)", hintSection, string(prevContent), string(reviewContent)); err != nil {
		return err
	}

	if err := runClaude(plannerInput, generatedPath); err != nil {
		return fmt.Errorf("Claude 実行中に失敗しました (.llm-duet/workspace/plan_generated.md を確認してください): %w", err)
	}

	if err := copyFile(generatedPath, planPath); err != nil {
		return err
	}

	return nil
}

func writePlannerInput(path, ideaText, hintText, currentPlan, reviewJSON string) error {
	if ideaText == "" {
		ideaText = "(none)"
	}
	if hintText == "" {
		hintText = "(none)"
	}
	if currentPlan == "" {
		currentPlan = "(none)"
	}
	if reviewJSON == "" {
		reviewJSON = "{}"
	}

	builder := &strings.Builder{}
	builder.WriteString("--- IDEA ---\n")
	builder.WriteString(ideaText)
	builder.WriteString("\n\n--- HINT ---\n")
	builder.WriteString(hintText)
	builder.WriteString("\n\n--- CURRENT_PLAN ---\n")
	builder.WriteString(currentPlan)
	builder.WriteString("\n\n--- REVIEW_JSON ---\n")
	builder.WriteString(reviewJSON)
	builder.WriteString("\n")

	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func writeReviewerInput(path, promptPath, planPath string) error {
	promptContent, err := os.ReadFile(promptPath)
	if err != nil {
		return err
	}
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}

builder := &strings.Builder{}
builder.WriteString("[REVIEW_PROMPT]\n")
builder.WriteString(string(promptContent))
builder.WriteString("\n\n--- PLAN_MD ---\n")
builder.WriteString(string(planContent))
builder.WriteString("\n")

	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func runClaude(inputPath, outputPath string) error {
	inFile, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer inFile.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	cmd := exec.Command("claude", "-p", "--system-prompt-file", filepath.Join(".llm-duet", "prompts", "planner.txt"))
	cmd.Stdin = inFile
	cmd.Stdout = outFile
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("LLM CLI 実行に失敗しました: %v: %s", err, stderr.String())
	}
	return nil
}

func runCodex(inputPath, outputPath string) error {
	inFile, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer inFile.Close()

	cmd := exec.Command("codex", "exec", "--cd", ".", "--skip-git-repo-check", "--output-last-message", outputPath, "-")
	cmd.Stdin = inFile
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdout = devNull
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("LLM CLI 実行に失敗しました: %v: %s", err, stderr.String())
	}
	return nil
}

func checkCLIs() error {
	if _, err := exec.LookPath("claude"); err != nil {
		return errors.New("Claude Code CLI (claude) が見つかりません。インストールとログインを行ってから llm-duet を実行してください。")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return errors.New("Codex CLI (codex) が見つかりません。インストールとログインを行ってから llm-duet を実行してください。")
	}
	return nil
}

func readOptionalFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func mergeHint(hint, hintFile string) (string, error) {
	base, err := readOptionalFile(hintFile)
	if err != nil {
		return "", err
	}
	if hint == "" {
		return base, nil
	}
	if base == "" {
		return hint, nil
	}
	return base + "\n\n" + hint, nil
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func enumerateUnique(items []string) []string {
	seen := map[string]struct{}{}
	var res []string
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		res = append(res, item)
	}
	return res
}

const defaultPlannerPrompt = `You are the planner role. The input always contains four sections separated by lines that exactly match:
--- IDEA ---
--- HINT ---
--- CURRENT_PLAN ---
--- REVIEW_JSON ---

Rewrite plan.md by using CURRENT_PLAN as the base, reflecting any constraints or directions in HINT, and addressing feedback inside REVIEW_JSON.
Respect the overall intent from IDEA when it is provided, avoid expanding scope unnecessarily, and output only the updated plan.md content.`

const defaultReviewerPrompt = `You are the reviewer. After the [REVIEW_PROMPT] marker, review instructions are provided, followed by --- PLAN_MD --- and the body to inspect.
Respond with JSON that matches the expected schema (comments array with id, severity, kind, summary, question_to_human, auto_fix_proposal fields).
Focus on clarity, scope control, and actionable feedback.`

// validateReviewJSON ensures the JSON matches the expected schema.
func validateReviewJSON(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("JSON パースに失敗しました: %w", err)
	}

	commentsRaw, ok := root["comments"]
	if !ok {
		return errors.New("comments フィールドが存在しません")
	}

	var comments []json.RawMessage
	if err := json.Unmarshal(commentsRaw, &comments); err != nil {
		return fmt.Errorf("comments フィールドの形式が不正です: %w", err)
	}

	allowedSeverity := map[string]struct{}{"critical": {}, "major": {}, "minor": {}}
	allowedKind := map[string]struct{}{"spec_intent": {}, "feature_scope": {}, "api_design": {}, "ux_flow": {}, "performance": {}, "cost": {}, "style_minor": {}}

	for i, cRaw := range comments {
		var cMap map[string]json.RawMessage
		if err := json.Unmarshal(cRaw, &cMap); err != nil {
			return fmt.Errorf("comments[%d] のパースに失敗しました: %w", i, err)
		}

		requiredKeys := []string{"id", "severity", "kind", "summary", "question_to_human", "auto_fix_proposal"}
		for _, k := range requiredKeys {
			if _, ok := cMap[k]; !ok {
				return fmt.Errorf("comments[%d] に %s がありません", i, k)
			}
		}

		var id, severity, kind, summary string
		if err := json.Unmarshal(cMap["id"], &id); err != nil || id == "" {
			return fmt.Errorf("comments[%d] の id が不正です", i)
		}
		if err := json.Unmarshal(cMap["severity"], &severity); err != nil {
			return fmt.Errorf("comments[%d] の severity が不正です", i)
		}
		if _, ok := allowedSeverity[severity]; !ok {
			return fmt.Errorf("comments[%d] の severity が不正な値です", i)
		}
		if err := json.Unmarshal(cMap["kind"], &kind); err != nil {
			return fmt.Errorf("comments[%d] の kind が不正です", i)
		}
		if _, ok := allowedKind[kind]; !ok {
			return fmt.Errorf("comments[%d] の kind が不正な値です", i)
		}
		if err := json.Unmarshal(cMap["summary"], &summary); err != nil || summary == "" {
			return fmt.Errorf("comments[%d] の summary が不正です", i)
		}

		for _, key := range []string{"question_to_human", "auto_fix_proposal"} {
			v := cMap[key]
			if string(v) == "null" {
				continue
			}
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return fmt.Errorf("comments[%d] の %s が不正です", i, key)
			}
		}
	}

	return nil
}

func writeIfNotExists(path, content string) error {
	if fileExists(path) {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

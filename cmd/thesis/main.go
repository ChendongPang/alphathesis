// thesis is a CLI tool for creating and managing investment theses in AlphaThesis.
//
// It parses raw free-form thesis text through ThesisParserAgent, saves the
// structured result to the database, embeds assumptions, and optionally
// enqueues a daily_run job so the runner processes it immediately.
//
// Usage:
//
//	echo "I think NVDA will outperform..." | thesis -email user@example.com [-name "Alice"] [-yes] [-run]
//	thesis -email user@example.com -yes -run < thesis.txt
//
// Required env vars:
//
//	DATABASE_URL   PostgreSQL DSN
//
// Optional env vars (shown with defaults):
//
//	VLLM_CHAT_URL   http://localhost:8000/v1   vLLM chat completions base URL
//	VLLM_EMBED_URL  http://localhost:8001/v1   vLLM embeddings base URL
//	CHAT_MODEL      Qwen/Qwen3-8B
//	EMBED_MODEL     Qwen/Qwen3-Embedding-0.6B
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"alphathesis/agent/parser"
	"alphathesis/client"
	eng "alphathesis/engine"
	"alphathesis/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var (
		email   = flag.String("email", "", "User email (required)")
		name    = flag.String("name", "", "User display name")
		skipYes = flag.Bool("yes", false, "Skip confirmation prompt")
		enqueue = flag.Bool("run", false, "Also enqueue a daily_run job for today")
	)
	flag.Parse()

	if strings.TrimSpace(*email) == "" {
		fmt.Fprintln(os.Stderr, "error: -email is required")
		flag.Usage()
		os.Exit(1)
	}

	// ── Read raw thesis text from stdin ──────────────────────────
	rawText, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Error("read stdin", "err", err)
		os.Exit(1)
	}
	if strings.TrimSpace(string(rawText)) == "" {
		fmt.Fprintln(os.Stderr, "error: no thesis text on stdin")
		os.Exit(1)
	}

	dbDSN := mustEnv("DATABASE_URL")
	chatURL := envOr("VLLM_CHAT_URL", "http://localhost:8000/v1")
	embedURL := envOr("VLLM_EMBED_URL", "http://localhost:8001/v1")
	chatModel := envOr("CHAT_MODEL", "Qwen/Qwen3-8B")
	embedModel := envOr("EMBED_MODEL", "Qwen/Qwen3-Embedding-0.6B")

	ctx := context.Background()

	// ── vLLM clients ─────────────────────────────────────────────
	chatClient, err := client.NewVLLMClient(chatURL, "",
		client.WithTimeout(120*time.Second),
	)
	if err != nil {
		log.Error("create chat client", "err", err)
		os.Exit(1)
	}
	embedClient, err := client.NewVLLMClient(embedURL, "",
		client.WithTimeout(60*time.Second),
	)
	if err != nil {
		log.Error("create embed client", "err", err)
		os.Exit(1)
	}

	// ── Parse thesis ─────────────────────────────────────────────
	parserAgent, err := parser.NewThesisParserAgent(chatClient, chatModel)
	if err != nil {
		log.Error("create parser agent", "err", err)
		os.Exit(1)
	}

	log.Info("parsing thesis...")
	parsed, err := parserAgent.Parse(ctx, string(rawText))
	if err != nil {
		log.Error("parse thesis", "err", err)
		os.Exit(1)
	}
	if err := parsed.Normalize(); err != nil {
		log.Error("normalize parsed thesis", "err", err)
		os.Exit(1)
	}

	// ── Display parsed result ─────────────────────────────────────
	fmt.Println()
	fmt.Println("═══ Parsed Thesis ═══════════════════════════════════════")
	fmt.Printf("  Symbol:      %s\n", parsed.Symbol)
	fmt.Printf("  Company:     %s\n", parsed.CompanyName)
	fmt.Printf("  Direction:   %s\n", parsed.Direction)
	fmt.Printf("  Core claim:  %s\n", parsed.CoreClaim)
	fmt.Printf("  Assumptions: %d\n", len(parsed.Assumptions))
	fmt.Println()
	for i, a := range parsed.Assumptions {
		fmt.Printf("  [%d] %s  (type=%s importance=%.2f)\n", i+1, a.Key, a.Type, a.Importance)
		fmt.Printf("      %s\n", a.Text)
		if len(a.EvidenceHints) > 0 {
			fmt.Printf("      hints: %s\n", strings.Join(a.EvidenceHints, ", "))
		}
	}
	fmt.Println("═════════════════════════════════════════════════════════")
	fmt.Println()

	// ── Confirm ───────────────────────────────────────────────────
	if !*skipYes {
		fmt.Print("Save this thesis? [y/N] ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			fmt.Fprintln(os.Stderr, "aborted")
			os.Exit(1)
		}
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(os.Stderr, "aborted")
			os.Exit(0)
		}
	}

	// ── Database ──────────────────────────────────────────────────
	db, err := store.NewDB(ctx, dbDSN)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	thesisStore := store.NewThesisStore(db)
	jobStore := store.NewJobStore(db)

	// ── Create user ───────────────────────────────────────────────
	user, err := thesisStore.GetOrCreateUser(ctx, *email, *name)
	if err != nil {
		log.Error("get/create user", "err", err)
		os.Exit(1)
	}
	log.Info("user ready", "user_id", user.ID, "email", user.Email)

	// ── Create thesis ─────────────────────────────────────────────
	assumptionParams := make([]store.AssumptionParams, len(parsed.Assumptions))
	for i, a := range parsed.Assumptions {
		assumptionParams[i] = store.AssumptionParams{
			AssumptionKey: a.Key,
			Text:          a.Text,
			Type:          a.Type,
			Verifiable:    a.Verifiable,
			Importance:    a.Importance,
			EvidenceHints: a.EvidenceHints,
		}
	}

	thesis, err := thesisStore.CreateThesis(ctx, store.CreateThesisParams{
		UserID:        user.ID,
		Symbol:        parsed.Symbol,
		CompanyName:   parsed.CompanyName,
		Direction:     parsed.Direction,
		RawText:       string(rawText),
		CoreClaim:     parsed.CoreClaim,
		LLMModel:      chatModel,
		ParserVersion: "thesis_parser_v1",
		Assumptions:   assumptionParams,
	})
	if err != nil {
		log.Error("create thesis", "err", err)
		os.Exit(1)
	}
	log.Info("thesis created", "thesis_id", thesis.ID, "version", thesis.Version)

	// ── Embed assumptions ─────────────────────────────────────────
	assumptions, err := thesisStore.GetAssumptionsWithEmbeddings(ctx, thesis.ID)
	if err != nil {
		log.Error("get assumptions", "err", err)
		os.Exit(1)
	}

	assumptionEmbedder := eng.NewAssumptionEmbedder(embedClient, embedModel, thesisStore)
	if err := assumptionEmbedder.EmbedAssumptions(ctx, assumptions); err != nil {
		// Non-fatal: runner will retry on next job execution.
		log.Warn("assumption embedding failed (will retry on first run)", "err", err)
	} else {
		log.Info("assumptions embedded", "count", len(assumptions))
	}

	// ── Enqueue job ───────────────────────────────────────────────
	if *enqueue {
		job, err := jobStore.CreateJobRun(ctx, thesis.ID, thesis.Version, time.Now(), store.JobTypeDailyRun)
		if err != nil {
			log.Error("create job run", "err", err)
			os.Exit(1)
		}
		log.Info("job enqueued", "job_id", job.ID, "run_date", job.RunDate)
		fmt.Printf("\nthesis_id=%d  job_id=%d\n", thesis.ID, job.ID)
	} else {
		fmt.Printf("\nthesis_id=%d\n", thesis.ID)
		fmt.Println("(use -run to also enqueue a daily_run job)")
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: required env var %q not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

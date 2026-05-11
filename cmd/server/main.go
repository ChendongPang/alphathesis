// server is the AlphaThesis HTTP API server.
//
// Required env vars:
//
//	DATABASE_URL   PostgreSQL DSN
//
// Optional env vars (shown with defaults):
//
//	PORT                   8080
//	LOG_FILE               alphathesis.log   (debug log path; "" disables file log)
//	LLM_LOG_FILE           llm.log           (LLM request/response log; "" disables)
//	DEFAULT_USER_EMAIL     admin@alphathesis.local
//	DEFAULT_USER_NAME      AlphaThesis
//	VLLM_CHAT_URL          http://localhost:8000/v1
//	VLLM_EMBED_URL         http://localhost:8001/v1
//	CHAT_MODEL             Qwen/Qwen3-8B
//	EMBED_MODEL            Qwen/Qwen3-Embedding-0.6B
//	PYADAPTER_URL          http://localhost:8811
//	SEC_USER_AGENT         AlphaThesis server@alphathesis.local
//	WEB_DIST_DIR           web/dist
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"alphathesis/agent/dedup"
	"alphathesis/agent/evidence"
	"alphathesis/agent/parser"
	"alphathesis/agent/relevance"
	reportagent "alphathesis/agent/report"
	"alphathesis/client"
	"alphathesis/datasource"
	cninfoSource "alphathesis/datasource/cn/cninfo"
	cndatasource "alphathesis/datasource/cn/pyadapter"
	usdatasource "alphathesis/datasource/us"
	eng "alphathesis/engine"
	"alphathesis/engine/rag"
	"alphathesis/engine/runner"
	"alphathesis/store"
)

// ------------------------------------------------------------
// JSON response types (camelCase for JS compatibility)
// ------------------------------------------------------------

type thesisItem struct {
	ID              int64     `json:"id"`
	Symbol          string    `json:"symbol"`
	CompanyName     string    `json:"companyName"`
	Market          string    `json:"market"`
	Direction       string    `json:"direction"`
	CoreClaim       string    `json:"coreClaim"`
	ConfidenceScore float64   `json:"confidenceScore"`
	AlertLevel      string    `json:"alertLevel"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type assumptionItem struct {
	ID           int64   `json:"id"`
	Key          string  `json:"key"`
	Text         string  `json:"text"`
	Type         string  `json:"type"`
	Importance   float64 `json:"importance"`
	CurrentScore float64 `json:"currentScore"`
	PosCount     int     `json:"posCount"`
	NegCount     int     `json:"negCount"`
	NeutralCount int     `json:"neutralCount"`
}

type thesisDetailResp struct {
	thesisItem
	Assumptions []assumptionItem `json:"assumptions"`
}

type scorePoint struct {
	Date       string  `json:"date"`
	ScoreAfter float64 `json:"score"`
}

type reportItem struct {
	ID                int64   `json:"id"`
	RunDate           string  `json:"runDate"`
	Title             string  `json:"title"`
	Summary           string  `json:"summary"`
	ThesisScoreBefore float64 `json:"thesisScoreBefore"`
	ThesisScoreAfter  float64 `json:"thesisScoreAfter"`
	ThesisScoreDelta  float64 `json:"thesisScoreDelta"`
	AlertLevel        string  `json:"alertLevel"`
	SnippetCount      int     `json:"snippetCount"`
}

type snippetItem struct {
	ID              int64      `json:"id"`
	AssumptionID    int64      `json:"assumptionId"`
	CandidateSource string     `json:"candidateSource"`
	CandidateURL    string     `json:"candidateUrl"`
	CandidateTitle  string     `json:"candidateTitle"`
	SnippetText     string     `json:"snippetText"`
	Stance          string     `json:"stance"`
	Impact          float64    `json:"impact"`
	Confidence      float64    `json:"confidence"`
	PublishedAt     *time.Time `json:"publishedAt"`
}

type reportDetailResp struct {
	reportItem
	MarkdownReport string          `json:"markdownReport"`
	MarketContext  json.RawMessage `json:"marketContext"`
	Snippets       []snippetItem   `json:"snippets"`
}

type sseEvent struct {
	Kind string `json:"kind"` // section | pending | ok | error | done
	Text string `json:"text"`
	ID   *int64 `json:"id,omitempty"`
}

// ------------------------------------------------------------
// Server
// ------------------------------------------------------------

type srv struct {
	log           *slog.Logger
	thesisStore   *store.ThesisStore
	scoreStore    *store.ScoreStore
	evidenceStore *store.EvidenceStore
	jobStore      *store.JobStore
	parser        *parser.ThesisParserAgent
	runner        *runner.Runner
	sessions      *sessionStore
}

func main() {
	// ── Logging: stderr + optional file ────────────────────────
	logFile := envOr("LOG_FILE", "alphathesis.log")
	var logWriter io.Writer = os.Stderr
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			slog.Error("open log file", "path", logFile, "err", err)
			// non-fatal: continue with stderr only
		} else {
			defer f.Close()
			logWriter = io.MultiWriter(os.Stderr, f)
		}
	}
	log := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// ── LLM debug log ─────────────────────────────────────────
	llmLogFile := envOr("LLM_LOG_FILE", "llm.log")
	var llmLogWriter io.Writer
	if llmLogFile != "" {
		lf, err := os.OpenFile(llmLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Warn("open llm log file", "path", llmLogFile, "err", err)
		} else {
			defer lf.Close()
			llmLogWriter = lf
			log.Info("LLM debug log enabled", "path", llmLogFile)
		}
	}

	dbDSN := mustEnv("DATABASE_URL")
	port := envOr("PORT", "8080")
	chatURL := envOr("VLLM_CHAT_URL", "http://localhost:8000/v1")
	embedURL := envOr("VLLM_EMBED_URL", "http://localhost:8001/v1")
	chatModel := envOr("CHAT_MODEL", "Qwen/Qwen3-8B")
	embedModel := envOr("EMBED_MODEL", "Qwen/Qwen3-Embedding-0.6B")
	pyAdapterURL := envOr("PYADAPTER_URL", "http://localhost:8811")
	webDistDir := envOr("WEB_DIST_DIR", filepath.Join("web", "dist"))

	ctx := context.Background()

	// ── Database ─────────────────────────────────────────────────
	db, err := store.NewDB(ctx, dbDSN)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	jobStore := store.NewJobStore(db)
	thesisStore := store.NewThesisStore(db)
	candidateStore := store.NewCandidateStore(db)
	evidenceStore := store.NewEvidenceStore(db)
	scoreStore := store.NewScoreStore(db)

	// ── vLLM clients ──────────────────────────────────────────────
	chatClientOpts := []client.ClientOption{client.WithTimeout(120 * time.Second)}
	if llmLogWriter != nil {
		chatClientOpts = append(chatClientOpts, client.WithDebugLog(llmLogWriter))
	}
	chatClient, err := client.NewVLLMClient(chatURL, "", chatClientOpts...)
	if err != nil {
		log.Error("create chat client", "err", err)
		os.Exit(1)
	}
	embedClient, err := client.NewVLLMClient(embedURL, "", client.WithTimeout(60*time.Second))
	if err != nil {
		log.Error("create embed client", "err", err)
		os.Exit(1)
	}

	// ── yfinance MCP (shared: symbol resolver + news/quotes) ─────
	yfinanceMCPURL := envOr("YFINANCE_MCP_URL", "http://localhost:8812/mcp")
	var yfConn *client.MCPClient
	for attempt := 1; ; attempt++ {
		yfConn, err = client.NewMCPStreamableHTTPClient(ctx, yfinanceMCPURL)
		if err == nil {
			break
		}
		if attempt >= 10 {
			log.Error("connect yfinance MCP", "url", yfinanceMCPURL, "err", err)
			os.Exit(1)
		}
		log.Warn("yfinance MCP not ready, retrying", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
	yfClient, err := usdatasource.NewYFinanceMCPClient(yfConn)
	if err != nil {
		log.Error("create yfinance MCP client", "err", err)
		os.Exit(1)
	}
	hkUsResolver, err := parser.NewMCPSymbolResolver(yfConn, "search_symbol")
	if err != nil {
		log.Error("create symbol resolver", "err", err)
		os.Exit(1)
	}
	cnResolver := parser.NewAKShareSymbolResolver(pyAdapterURL)
	log.Info("yfinance MCP ready", "url", yfinanceMCPURL)

	// ── Parser agent ──────────────────────────────────────────────
	var parserAgent *parser.ThesisParserAgent
	parserAgent, err = parser.NewThesisParserAgent(chatClient, chatModel,
		parser.WithCNSymbolResolver(cnResolver),
		parser.WithHKUSSymbolResolver(hkUsResolver),
	)
	if err != nil {
		log.Warn("parser agent unavailable", "err", err)
		parserAgent = nil
	} else {
		log.Info("parser agent ready", "model", chatModel)
	}

	// ── Pipeline agents + engines ─────────────────────────────────
	relevanceJudge, err := relevance.NewJudge(chatClient, chatModel)
	if err != nil {
		log.Error("create relevance judge", "err", err)
		os.Exit(1)
	}
	dedupJudge, err := dedup.NewJudge(chatClient, chatModel)
	if err != nil {
		log.Error("create dedup judge", "err", err)
		os.Exit(1)
	}
	evidenceJudge, err := evidence.NewJudge(chatClient, chatModel)
	if err != nil {
		log.Error("create evidence judge", "err", err)
		os.Exit(1)
	}
	reporter := reportagent.New(chatClient, chatModel)

	assumptionEmbedder := eng.NewAssumptionEmbedder(embedClient, embedModel, thesisStore)
	ragEngine := rag.New(embedClient, embedModel)
	scoreEngine := eng.NewScoreEngine(eng.ScoreConfig{})
	marketEngine := eng.NewMarketContextEngine(eng.MarketContextConfig{})

	// ── CN datasource ─────────────────────────────────────────────
	pyClient := cndatasource.NewClient(cndatasource.WithBaseURL(pyAdapterURL))
	textFetcher := &datasource.RoutingFullTextFetcher{
		PDF:  pyClient,
		HTML: usdatasource.NewFullTextFetcher(),
	}

	var fetchers []runner.MarketFetcher
	fetchers = append(fetchers, runner.MarketFetcher{Market: "cn", Type: "news", Fetcher: pyClient})
	fetchers = append(fetchers, runner.MarketFetcher{Market: "cn", Type: "event", Fetcher: cninfoSource.NewCNInfoClient()})

	// ── US datasource: reuse yfClient already created above ──────
	fetchers = append(fetchers, runner.MarketFetcher{Market: "us", Type: "news", Fetcher: yfClient})
	log.Info("yfinance MCP news fetcher registered")

	secUA := envOr("SEC_USER_AGENT", "AlphaThesis server@alphathesis.local")
	secClient, err := usdatasource.NewSECClient(secUA)
	if err != nil {
		log.Error("create SEC client", "err", err)
		os.Exit(1)
	}
	fetchers = append(fetchers, runner.MarketFetcher{Market: "us", Type: "event", Fetcher: secClient})

	priceQuotes := &routingPriceQuoteFetcher{cn: pyClient, us: yfClient}

	// ── Runner ────────────────────────────────────────────────────
	r := runner.New(log, runner.Config{
		LLMModel:                 chatModel,
		EmbedModel:               embedModel,
		RAGTopK:                  3,
		DedupSearchLimit:         10,
		FetchLookback:            48 * time.Hour,
		TopSnippetsPerAssumption: 3,
	}, runner.Deps{
		JobStore:       jobStore,
		ThesisStore:    thesisStore,
		CandidateStore: candidateStore,
		EvidenceStore:  evidenceStore,
		ScoreStore:     scoreStore,

		Fetchers:    fetchers,
		TextFetcher: textFetcher,

		RelevanceJudge:     relevanceJudge,
		DedupJudge:         dedupJudge,
		EvidenceJudge:      evidenceJudge,
		AssumptionEmbedder: assumptionEmbedder,
		RAGEngine:          ragEngine,
		ScoreEngine:        scoreEngine,
		MarketEngine:       marketEngine,
		Reporter:           reporter,

		Embedder:   embedClient,
		EmbedModel: embedModel,

		PriceQuotes: priceQuotes,
	})

	s := &srv{
		log:           log,
		thesisStore:   thesisStore,
		scoreStore:    scoreStore,
		evidenceStore: evidenceStore,
		jobStore:      jobStore,
		parser:        parserAgent,
		runner:        r,
		sessions:      newSessionStore(),
	}

	auth := func(h http.HandlerFunc) http.Handler { return s.requireAuth(h) }

	mux := http.NewServeMux()

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// Protected routes
	mux.Handle("GET /api/me", auth(s.handleMe))
	mux.Handle("GET /api/theses", auth(s.listTheses))
	mux.Handle("GET /api/theses/{id}", auth(s.getThesis))
	mux.Handle("GET /api/theses/{id}/score-history", auth(s.getScoreHistory))
	mux.Handle("GET /api/theses/{id}/reports", auth(s.listReports))
	mux.Handle("GET /api/theses/{id}/reports/{rid}", auth(s.getReport))
	mux.Handle("GET /api/theses/{id}/evidence", auth(s.listEvidence))
	mux.Handle("DELETE /api/theses/{id}", auth(s.deleteThesis))
	mux.Handle("POST /api/theses/{id}/run", auth(s.triggerRun))
	mux.Handle("POST /api/theses/parse-stream", auth(s.parseStream))
	mux.Handle("GET /", spaHandler(webDistDir, log))

	go s.scheduleDailyRuns(log)

	log.Info("server listening", "port", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}

// ------------------------------------------------------------
// Handlers
// ------------------------------------------------------------

func (s *srv) listTheses(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFromContext(r.Context())
	theses, err := s.thesisStore.ListActiveThesesByUser(r.Context(), userID)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	items := make([]thesisItem, 0, len(theses))
	for _, t := range theses {
		item := toThesisItem(t)
		if dr, err := s.scoreStore.GetLatestDailyReport(r.Context(), t.ID); err == nil && dr != nil {
			item.AlertLevel = dr.AlertLevel
		}
		items = append(items, item)
	}
	jsonOK(w, items)
}

func (s *srv) getThesis(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}

	t, err := s.thesisStore.GetThesis(r.Context(), id)
	if err != nil || t == nil {
		if t == nil {
			jsonErr(w, errors.New("thesis not found"), 404)
		} else {
			jsonErr(w, err, 500)
		}
		return
	}

	assumptions, err := s.thesisStore.GetAssumptions(r.Context(), id)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	counts, _ := s.scoreStore.ListAssumptionEvidenceCounts(r.Context(), id)
	countMap := make(map[int64]store.AssumptionEvidenceCounts, len(counts))
	for _, c := range counts {
		countMap[c.AssumptionID] = c
	}

	aItems := make([]assumptionItem, 0, len(assumptions))
	for _, a := range assumptions {
		c := countMap[a.ID]
		aItems = append(aItems, assumptionItem{
			ID:           a.ID,
			Key:          a.AssumptionKey,
			Text:         a.Text,
			Type:         a.Type,
			Importance:   a.Importance,
			CurrentScore: a.CurrentScore,
			PosCount:     c.PosCount,
			NegCount:     c.NegCount,
			NeutralCount: c.NeutralCount,
		})
	}

	item := toThesisItem(t)
	if dr, err := s.scoreStore.GetLatestDailyReport(r.Context(), id); err == nil && dr != nil {
		item.AlertLevel = dr.AlertLevel
	}

	jsonOK(w, thesisDetailResp{thesisItem: item, Assumptions: aItems})
}

func (s *srv) getScoreHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}

	limit := 90
	histories, err := s.scoreStore.ListThesisScoreHistory(r.Context(), id, limit)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	// ListThesisScoreHistory returns newest-first; reverse for chart (oldest first).
	points := make([]scorePoint, len(histories))
	for i, h := range histories {
		points[len(histories)-1-i] = scorePoint{
			Date:       h.RunDate.Format("01-02"),
			ScoreAfter: h.ScoreAfter,
		}
	}
	jsonOK(w, points)
}

func (s *srv) listReports(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}

	userID, _ := userIDFromContext(r.Context())
	reports, err := s.scoreStore.ListDailyReportsByUser(r.Context(), userID, id, 50)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	items := make([]reportItem, 0, len(reports))
	for _, dr := range reports {
		count, _ := s.evidenceStore.CountEvidenceSnippetsByThesisAndDate(r.Context(), id, dr.RunDate)
		items = append(items, reportItem{
			ID:                dr.ID,
			RunDate:           dr.RunDate.Format("2006-01-02"),
			Title:             dr.Title,
			Summary:           dr.Summary,
			ThesisScoreBefore: dr.ThesisScoreBefore,
			ThesisScoreAfter:  dr.ThesisScoreAfter,
			ThesisScoreDelta:  dr.ThesisScoreDelta,
			AlertLevel:        dr.AlertLevel,
			SnippetCount:      count,
		})
	}
	jsonOK(w, items)
}

func (s *srv) getReport(w http.ResponseWriter, r *http.Request) {
	thesisID, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	reportID, ok := pathInt64(w, r, "rid")
	if !ok {
		return
	}

	userID, _ := userIDFromContext(r.Context())
	t, err := s.thesisStore.GetThesis(r.Context(), thesisID)
	if err != nil || t == nil || t.UserID != userID {
		jsonErr(w, errors.New("report not found"), 404)
		return
	}

	dr, err := s.scoreStore.GetDailyReportByID(r.Context(), reportID)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}
	if dr == nil || dr.ThesisID != thesisID {
		jsonErr(w, errors.New("report not found"), 404)
		return
	}

	snips, err := s.evidenceStore.ListEvidenceSnippetsByThesisAndDate(r.Context(), thesisID, dr.RunDate, 50)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	count, _ := s.evidenceStore.CountEvidenceSnippetsByThesisAndDate(r.Context(), thesisID, dr.RunDate)

	jsonOK(w, reportDetailResp{
		reportItem: reportItem{
			ID:                dr.ID,
			RunDate:           dr.RunDate.Format("2006-01-02"),
			Title:             dr.Title,
			Summary:           dr.Summary,
			ThesisScoreBefore: dr.ThesisScoreBefore,
			ThesisScoreAfter:  dr.ThesisScoreAfter,
			ThesisScoreDelta:  dr.ThesisScoreDelta,
			AlertLevel:        dr.AlertLevel,
			SnippetCount:      count,
		},
		MarkdownReport: dr.MarkdownReport,
		MarketContext:  dr.MarketContext,
		Snippets:       toSnippetItems(snips),
	})
}

func (s *srv) listEvidence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}

	filter := r.URL.Query().Get("filter") // "event" | "news" | "" (all)

	snips, err := s.evidenceStore.ListEvidenceSnippetsByThesis(r.Context(), id, 100)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	var filtered []*store.EvidenceSnippet
	for _, sn := range snips {
		switch filter {
		case "event":
			if sn.CandidateSource == "cn_official_cninfo" || sn.CandidateSource == "us_sec" {
				filtered = append(filtered, sn)
			}
		case "news":
			if strings.Contains(sn.CandidateSource, "news") {
				filtered = append(filtered, sn)
			}
		default:
			filtered = append(filtered, sn)
		}
	}

	jsonOK(w, toSnippetItems(filtered))
}

func (s *srv) deleteThesis(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	userID, _ := userIDFromContext(r.Context())
	t, err := s.thesisStore.GetThesis(r.Context(), id)
	if err != nil || t == nil || t.UserID != userID {
		jsonErr(w, errors.New("thesis not found"), 404)
		return
	}
	if err := s.thesisStore.SoftDeleteThesis(r.Context(), id); err != nil {
		jsonErr(w, err, 500)
		return
	}
	_ = s.jobStore.CancelJobRunsForThesis(r.Context(), id)
	jsonOK(w, map[string]any{"deleted": true})
}

func (s *srv) triggerRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}

	t, err := s.thesisStore.GetThesis(r.Context(), id)
	if err != nil || t == nil {
		jsonErr(w, errors.New("thesis not found"), 404)
		return
	}

	job, err := s.jobStore.CreateJobRun(r.Context(), id, t.Version, time.Now(), store.JobTypeDailyRun)
	if err != nil {
		jsonErr(w, err, 500)
		return
	}

	jsonOK(w, map[string]any{"jobId": job.ID, "status": job.Status})
}

func (s *srv) parseStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", 400)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)

	requestID := time.Now().UnixNano()
	userID, _ := userIDFromContext(r.Context())
	s.log.Debug("parse stream started", "request_id", requestID, "user_id", userID)
	defer s.log.Debug("parse stream handler returned", "request_id", requestID)

	emit := func(ev sseEvent) bool {
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			s.log.Warn("parse stream write failed", "request_id", requestID, "kind", ev.Kind, "err", err)
			return false
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
			s.log.Warn("parse stream request context canceled after emit", "request_id", requestID, "kind", ev.Kind, "err", r.Context().Err())
			return false
		default:
			return true
		}
	}

	if s.parser == nil {
		emit(sseEvent{Kind: "error", Text: "LLM 不可用，请先启动 vLLM 服务"})
		return
	}

	// ── Step 1: parse thesis text ─────────────────────────────────
	if !emit(sseEvent{Kind: "section", Text: "解析输入"}) {
		return
	}
	if !emit(sseEvent{Kind: "pending", Text: "调用 LLM 解析 thesis ..."}) {
		return
	}

	parsed, err := s.parser.Parse(r.Context(), req.Text)
	if err != nil {
		emit(sseEvent{Kind: "error", Text: "解析失败: " + err.Error()})
		s.log.Warn("parse thesis failed", "request_id", requestID, "err", err)
		return
	}

	dirLabel := map[string]string{
		"bullish": "看多 · Bullish",
		"bearish": "看空 · Bearish",
		"neutral": "中性 · Neutral",
	}
	mktLabel := map[string]string{"cn": "CN · A股", "us": "US · 美股"}

	emit(sseEvent{Kind: "ok", Text: "标的  " + parsed.Symbol})
	emit(sseEvent{Kind: "ok", Text: "公司  " + parsed.CompanyName})
	emit(sseEvent{Kind: "ok", Text: "市场  " + mktLabel[parsed.Market]})
	emit(sseEvent{Kind: "ok", Text: "方向  " + dirLabel[parsed.Direction]})

	emit(sseEvent{Kind: "section", Text: "拆解假设条件"})
	for i, a := range parsed.Assumptions {
		emit(sseEvent{Kind: "ok", Text: fmt.Sprintf("Assumption %d  %s", i+1, a.Text)})
	}

	// ── Step 2: persist thesis to DB ──────────────────────────────
	emit(sseEvent{Kind: "section", Text: "初始化监控"})
	emit(sseEvent{Kind: "pending", Text: "写入数据库 ..."})

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

	thesis, err := s.thesisStore.CreateThesis(r.Context(), store.CreateThesisParams{
		UserID:      userID,
		Symbol:      parsed.Symbol,
		CompanyName: parsed.CompanyName,
		Market:      parsed.Market,
		Direction:   parsed.Direction,
		RawText:     req.Text,
		CoreClaim:   parsed.CoreClaim,
		LLMModel:    "",
		Assumptions: assumptionParams,
	})
	if err != nil {
		emit(sseEvent{Kind: "error", Text: "保存失败: " + err.Error()})
		return
	}

	createdID := thesis.ID
	emit(sseEvent{Kind: "ok", Text: fmt.Sprintf("论题 #%d 已创建，%d 条假设", thesis.ID, len(parsed.Assumptions)), ID: &createdID})

	// ── Step 3: create job and run pipeline inline ─────────────────
	emit(sseEvent{Kind: "section", Text: "运行分析流水线"})

	job, err := s.jobStore.CreateJobRun(r.Context(), thesis.ID, thesis.Version, time.Now(), store.JobTypeDailyRun)
	if err != nil {
		emit(sseEvent{Kind: "error", Text: "创建任务失败: " + err.Error()})
		return
	}
	s.log.Debug("inline pipeline job created", "thesis_id", thesis.ID, "job_id", job.ID)

	pipelineErr := s.runner.ProcessJobWithProgress(r.Context(), job, func(kind, text string) {
		emit(sseEvent{Kind: kind, Text: text})
	})
	if pipelineErr != nil {
		s.log.Error("inline pipeline failed", "thesis_id", thesis.ID, "job_id", job.ID, "err", pipelineErr)
		emit(sseEvent{Kind: "error", Text: "流水线错误: " + pipelineErr.Error()})
		// Don't return — still navigate to the newly created thesis page.
	}

	id := thesis.ID
	emit(sseEvent{Kind: "done", Text: "论题创建成功", ID: &id})
}

// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------

func toThesisItem(t *store.Thesis) thesisItem {
	return thesisItem{
		ID:              t.ID,
		Symbol:          t.Symbol,
		CompanyName:     t.CompanyName,
		Market:          t.Market,
		Direction:       t.Direction,
		CoreClaim:       t.CoreClaim,
		ConfidenceScore: t.ConfidenceScore,
		AlertLevel:      store.AlertLevelNone,
		Status:          t.Status,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	}
}

func toSnippetItems(snips []*store.EvidenceSnippet) []snippetItem {
	items := make([]snippetItem, 0, len(snips))
	for _, s := range snips {
		items = append(items, snippetItem{
			ID:              s.ID,
			AssumptionID:    s.AssumptionID,
			CandidateSource: s.CandidateSource,
			CandidateURL:    s.CandidateURL,
			CandidateTitle:  s.CandidateTitle,
			SnippetText:     s.SnippetText,
			Stance:          s.Stance,
			Impact:          s.Impact,
			Confidence:      s.Confidence,
			PublishedAt:     s.PublishedAt,
		})
	}
	return items
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func spaHandler(distDir string, log *slog.Logger) http.Handler {
	absDist, err := filepath.Abs(distDir)
	if err != nil {
		absDist = distDir
	}
	if st, err := os.Stat(absDist); err != nil || !st.IsDir() {
		log.Warn("web dist directory not found; frontend static hosting disabled", "path", absDist, "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "web dist not found; run `npm run build` in web/", http.StatusNotFound)
		})
	}

	fileServer := http.FileServer(http.Dir(absDist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		cleanPath := filepath.Clean(r.URL.Path)
		if cleanPath == "." || cleanPath == string(filepath.Separator) {
			cleanPath = "index.html"
		} else {
			cleanPath = strings.TrimPrefix(cleanPath, string(filepath.Separator))
		}

		fullPath := filepath.Join(absDist, cleanPath)
		if st, err := os.Stat(fullPath); err == nil && !st.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		http.ServeFile(w, r, filepath.Join(absDist, "index.html"))
	})
}

func pathInt64(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	v := r.PathValue(key)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		jsonErr(w, fmt.Errorf("invalid %s: %q", key, v), 400)
		return 0, false
	}
	return n, true
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required env var not set", "key", key)
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

// scheduleDailyRuns enqueues and processes jobs for all active theses every 24
// hours. It runs an initial pass immediately so the first run doesn't wait a
// full day after server start.
func (s *srv) scheduleDailyRuns(log *slog.Logger) {
	run := func() {
		ctx := context.Background()
		n, err := s.runner.EnqueueAll(ctx)
		if err != nil {
			log.Error("daily scheduler: enqueue failed", "err", err)
			return
		}
		if n == 0 {
			log.Info("daily scheduler: all theses already ran today")
			return
		}
		log.Info("daily scheduler: processing jobs", "enqueued", n)
		if err := s.runner.ProcessAll(ctx); err != nil {
			log.Error("daily scheduler: process failed", "err", err)
		}
	}

	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		run()
	}
}

// ------------------------------------------------------------
// Price quote routing (mirrors cmd/runner/main.go)
// ------------------------------------------------------------

type dailyQuoteFetcher interface {
	FetchDailyQuote(ctx context.Context, symbol string, tradingDay *time.Time) (eng.PriceQuote, error)
}

type routingPriceQuoteFetcher struct {
	cn dailyQuoteFetcher
	us dailyQuoteFetcher
}

func (f *routingPriceQuoteFetcher) FetchQuote(ctx context.Context, symbol string) (eng.PriceQuote, error) {
	if isHKSymbol(symbol) {
		if f.us != nil {
			return f.us.FetchDailyQuote(ctx, symbol, nil)
		}
		return eng.PriceQuote{Symbol: symbol}, nil
	}
	if isCNSymbol(symbol) {
		if f.cn != nil {
			return f.cn.FetchDailyQuote(ctx, symbol, nil)
		}
		return eng.PriceQuote{Symbol: symbol}, nil
	}
	if f.us != nil {
		return f.us.FetchDailyQuote(ctx, symbol, nil)
	}
	return eng.PriceQuote{Symbol: symbol}, nil
}

func isHKSymbol(symbol string) bool {
	return strings.HasSuffix(strings.ToUpper(symbol), ".HK")
}

// isCNSymbol returns true for A-shares (6 digits or .SS/.SZ suffix).
func isCNSymbol(symbol string) bool {
	upper := strings.ToUpper(symbol)
	if strings.HasSuffix(upper, ".SS") ||
		strings.HasSuffix(upper, ".SZ") {
		return true
	}
	if len(symbol) == 6 {
		for _, c := range symbol {
			if !unicode.IsDigit(c) {
				return false
			}
		}
		return true
	}
	return false
}

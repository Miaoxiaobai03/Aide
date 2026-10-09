package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aide/backend/internal/chat"
	"aide/backend/internal/coach"
	"aide/backend/internal/config"
	"aide/backend/internal/harness"
	"aide/backend/internal/httpapi"
	"aide/backend/internal/interview"
	"aide/backend/internal/jobextract"
	"aide/backend/internal/jobmatch"
	"aide/backend/internal/knowledge"
	"aide/backend/internal/llm"
	"aide/backend/internal/resumediagnosis"
	"aide/backend/internal/session"
	"aide/backend/internal/speech"
	"aide/backend/internal/webcrawler"
)

func main() {
	if _, err := config.LoadDotEnv(); err != nil {
		fatal("load environment", err)
	}
	logger := newLogger()

	knowledgeDirectory, err := resolveKnowledgeDirectory(envOr("KNOWLEDGE_DIR", "knowledge"))
	if err != nil {
		fatal("locate knowledge directory", err)
	}
	index, err := knowledge.Load(knowledgeDirectory)
	if err != nil {
		fatal("load knowledge index", err)
	}
	retriever, err := knowledge.NewInterviewRetriever(index, intEnv("AIDE_INTERVIEW_KNOWLEDGE_LIMIT", 8))
	if err != nil {
		fatal("create interview retriever", err)
	}
	databasePath, err := resolveDataPath(envOr("DB_PATH", "data/aide.db"))
	if err != nil {
		fatal("resolve interview database", err)
	}
	interviewStore, err := interview.OpenSQLiteStore(databasePath)
	if err != nil {
		fatal("open interview database", err)
	}
	defer func() {
		if closeErr := interviewStore.Close(); closeErr != nil {
			logger.Error("close interview database", "error", closeErr)
		}
	}()
	if interviewStore.BackupPath != "" {
		logger.Info("training migration backup created", "backup_path", interviewStore.BackupPath)
	}

	var interviewAgent interview.Agent
	var chatClient httpapi.ChatClient
	var crawlerAgent *webcrawler.Agent
	var matcherAgent *jobmatch.Agent
	var resumeDiagnosticianAgent *resumediagnosis.Agent
	var jdExtractor *jobextract.Agent
	modelConfigured := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
	if modelConfigured {
		modelClient, modelErr := llm.NewFromEnv()
		if modelErr != nil {
			fatal("configure text model", modelErr)
		}
		runtime, runtimeErr := harness.NewRuntime(modelClient, harness.Options{
			MaxConcurrent: intEnv("AIDE_HARNESS_MAX_CONCURRENT", 4),
			TraceCapacity: intEnv("AIDE_HARNESS_TRACE_CAPACITY", 512),
			TraceSink: func(event harness.TraceEvent) {
				if event.Type == harness.TraceError {
					logger.Warn("harness call failed", "trace_id", event.TraceID, "agent", event.AgentID, "tool", event.ToolName, "duration_ms", event.Duration.Milliseconds(), "error", event.Error)
				}
			},
		})
		if runtimeErr != nil {
			fatal("create harness runtime", runtimeErr)
		}
		interviewAgent, err = harness.NewInterviewAgent(runtime)
		if err != nil {
			fatal("register interview agents", err)
		}
		crawlerAgent, err = webcrawler.NewAgentWithOptions(runtime, webcrawler.NewFetcher(webcrawler.Options{
			RequestTimeout:       durationEnv("AIDE_CRAWLER_REQUEST_TIMEOUT", 10*time.Second),
			MaxResponseBytes:     int64(intEnv("AIDE_CRAWLER_MAX_RESPONSE_BYTES", 2<<20)),
			MaxRedirects:         intEnv("AIDE_CRAWLER_MAX_REDIRECTS", 3),
			AllowBenchmarkTunnel: boolEnv("AIDE_ALLOW_TUN_FAKE_IP", !strings.EqualFold(os.Getenv("NODE_ENV"), "production")),
		}), webcrawler.AgentOptions{
			MaxFallbackIterations: intEnv("AIDE_CRAWLER_FALLBACK_MAX_ITERATIONS", 4),
			FallbackTimeout:       durationEnv("AIDE_CRAWLER_FALLBACK_TIMEOUT", 90*time.Second),
			DecisionTimeout:       durationEnv("AIDE_CRAWLER_DECISION_TIMEOUT", 60*time.Second),
		})
		if err != nil {
			fatal("register web crawler agent", err)
		}
		matcherAgent, err = jobmatch.NewAgent(runtime, jobmatch.AgentOptions{
			Timeout: durationEnv("AIDE_MATCHER_TIMEOUT", 90*time.Second),
		})
		if err != nil {
			fatal("register resume matcher agent", err)
		}
		resumeDiagnosticianAgent, err = resumediagnosis.NewAgent(runtime, resumediagnosis.AgentOptions{
			Timeout: durationEnv("AIDE_RESUME_DIAGNOSTICIAN_TIMEOUT", 120*time.Second),
		})
		if err != nil {
			fatal("register resume diagnostician agent", err)
		}
		visionConfig := llm.ConfigFromEnv()
		visionConfig.Model = envOr("OPENAI_VISION_MODEL", visionConfig.Model)
		visionClient, visionErr := llm.New(visionConfig)
		if visionErr != nil {
			fatal("configure vision model", visionErr)
		}
		visionRuntime, visionErr := harness.NewRuntime(visionClient, harness.Options{
			MaxConcurrent: intEnv("AIDE_HARNESS_MAX_CONCURRENT", 4),
		})
		if visionErr != nil {
			fatal("create vision runtime", visionErr)
		}
		jdExtractor, err = jobextract.NewAgent(visionRuntime)
		if err != nil {
			fatal("register JD image transcriber", err)
		}
		chatClient, err = chat.New(chat.Config{
			APIKey:           os.Getenv("OPENAI_API_KEY"),
			BaseURL:          envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			Model:            envOr("OPENAI_MODEL", "gpt-5.5"),
			Timeout:          durationEnv("OPENAI_CHAT_TIMEOUT", 90*time.Second),
			MaxTokens:        intEnv("OPENAI_MAX_TOKENS", 4096),
			SummaryMaxTokens: intEnv("AIDE_SUMMARY_MAX_TOKENS", 8192), SummaryTimeout: durationEnv("AIDE_PAGE_SUMMARY_TIMEOUT", 45*time.Second), DisableSummaryThinking: ptrBool(boolEnv("AIDE_SUMMARY_DISABLE_THINKING", true)),
		}, nil)
		if err != nil {
			fatal("configure chat model", err)
		}
	} else {
		logger.Warn("text model is not configured; interview and chat endpoints are unavailable", "required_env", "OPENAI_API_KEY")
	}

	var speechClient httpapi.SpeechClient
	speechConfigured := strings.TrimSpace(os.Getenv("MIMO_API_KEY")) != ""
	if speechConfigured {
		speechClient, err = speech.New(speech.Config{
			APIKey:      os.Getenv("MIMO_API_KEY"),
			BaseURL:     envOr("MIMO_BASE_URL", "https://api.xiaomimimo.com/v1"),
			ASRModel:    envOr("MIMO_ASR_MODEL", "mimo-v2.5-asr"),
			TTSModel:    envOr("MIMO_TTS_MODEL", "mimo-v2.5-tts"),
			TTSVoice:    envOr("MIMO_TTS_VOICE", "mimo_default"),
			ASRLanguage: envOr("MIMO_ASR_LANGUAGE", "auto"),
			Timeout:     durationEnv("MIMO_TIMEOUT", 60*time.Second),
		}, nil)
		if err != nil {
			fatal("configure speech model", err)
		}
	}

	interviewService := interview.NewService(interview.Dependencies{
		Agent:     interviewAgent,
		Retriever: retriever,
		Store:     interviewStore,
	})
	authRequired := strings.EqualFold(os.Getenv("NODE_ENV"), "production") || boolEnv("AIDE_REQUIRE_AUTH", false)
	contextModel := envOr("OPENAI_MODEL", "gpt-5.5")
	windows := map[string]int{}
	if window := intEnv("AIDE_CONTEXT_WINDOW", 0); window > 0 {
		windows[contextModel] = window
	}
	coachService := coach.New(interviewStore, chatClient, index, coach.ContextConfig{
		SummaryThinkingMode: fmt.Sprint(boolEnv("AIDE_SUMMARY_DISABLE_THINKING", true)), DefaultModel: contextModel, InputCap: intEnv("AIDE_CONTEXT_INPUT_CAP", 30000), OutputReserve: intEnv("OPENAI_MAX_TOKENS", 4096), Safety: intEnv("AIDE_CONTEXT_SAFETY", 1024), Windows: windows,
		DisableSummaries: boolEnv("AIDE_DISABLE_CONTEXT_SUMMARIES", false), SummaryTimeout: durationEnv("AIDE_SUMMARY_TIMEOUT", 20*time.Second), PageSummaryTimeout: durationEnv("AIDE_PAGE_SUMMARY_TIMEOUT", 45*time.Second), SummaryOutputReserve: intEnv("AIDE_SUMMARY_MAX_TOKENS", 8192),
	})
	if recovered, err := coachService.RecoverAfterRestart(context.Background()); err != nil {
		fatal("recover interrupted practice operations", err)
	} else if recovered > 0 {
		logger.Info("interrupted practice operations recovered without model retries", "pages", recovered)
	}
	api, err := httpapi.New(httpapi.Config{
		Version:                 config.Version,
		APIKey:                  os.Getenv("AIDE_API_KEY"),
		RequireAuth:             authRequired,
		AllowedOrigins:          csvEnv("AIDE_ALLOWED_ORIGINS", []string{"http://localhost:3000", "http://127.0.0.1:3000"}),
		MaxJSONBodyBytes:        int64(intEnv("AIDE_MAX_JSON_BODY_BYTES", 256<<10)),
		MaxInterviewBytes:       int64(intEnv("AIDE_MAX_INTERVIEW_BODY_BYTES", 2<<20)),
		MaxAudioBodyBytes:       int64(intEnv("AIDE_MAX_AUDIO_BODY_BYTES", 25<<20)),
		MaxResumeDiagnosisBytes: int64(intEnv("AIDE_MAX_RESUME_DIAGNOSIS_BODY_BYTES", 12<<20)),
		MaxJDImageBodyBytes:     int64(intEnv("AIDE_MAX_JD_IMAGE_BODY_BYTES", 11<<20)),
		MaxMessageChars:         intEnv("AIDE_MAX_MESSAGE_CHARS", 20000),
		MaxTTSTextChars:         intEnv("AIDE_MAX_TTS_TEXT_CHARS", 5000),
		MaxURLChars:             intEnv("AIDE_MAX_URL_CHARS", 4096),
		KnowledgeEntries:        index.Len(),
		ModelConfigured:         modelConfigured,
		SpeechConfigured:        speechConfigured,
	}, httpapi.Dependencies{
		Coach:               coachService,
		Interview:           interviewService,
		Chat:                chatClient,
		Speech:              speechClient,
		Crawler:             crawlerAgent,
		Matcher:             matcherAgent,
		ResumeDiagnostician: resumeDiagnosticianAgent,
		JDExtractor:         jdExtractor,
		Sessions:            session.NewStore(),
		Memory:              session.NewMemory(40),
		Logger:              logger,
	})
	if err != nil {
		fatal("create HTTP API", err)
	}

	port := intEnv("PORT", 3001)
	bind := envOr("AIDE_API_BIND", "127.0.0.1")
	server := api.HTTPServer(net.JoinHostPort(bind, strconv.Itoa(port)))
	errChannel := make(chan error, 1)
	go func() {
		logger.Info("Aide Go API started",
			"version", config.Version,
			"port", port,
			"knowledge_entries", index.Len(),
			"knowledge_directory", knowledgeDirectory,
			"database_path", databasePath,
			"model_configured", modelConfigured,
			"speech_configured", speechConfigured,
			"crawler_configured", crawlerAgent != nil,
			"matcher_configured", matcherAgent != nil,
			"resume_diagnostician_configured", resumeDiagnosticianAgent != nil,
			"auth_required", authRequired,
		)
		if listenErr := server.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			errChannel <- listenErr
		}
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalContext.Done():
		logger.Info("shutdown requested", "signal", signalContext.Err())
	case listenErr := <-errChannel:
		fatal("serve HTTP", listenErr)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		_ = server.Close()
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func resolveKnowledgeDirectory(configured string) (string, error) {
	if filepath.IsAbs(configured) {
		if isDirectory(configured) {
			return filepath.Clean(configured), nil
		}
		return "", fmt.Errorf("%s does not exist or is not a directory", configured)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(workingDirectory, configured),
		filepath.Join(filepath.Dir(workingDirectory), configured),
	}
	for _, candidate := range candidates {
		if isDirectory(candidate) {
			absolute, absoluteErr := filepath.Abs(candidate)
			if absoluteErr != nil {
				return "", absoluteErr
			}
			return absolute, nil
		}
	}
	return "", fmt.Errorf("%s not found from %s or its parent", configured, workingDirectory)
}

func resolveDataPath(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", errors.New("database path is empty")
	}
	if filepath.IsAbs(configured) {
		if err := os.MkdirAll(filepath.Dir(configured), 0o750); err != nil {
			return "", err
		}
		return filepath.Clean(configured), nil
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	base := workingDirectory
	if strings.EqualFold(filepath.Base(workingDirectory), "backend") {
		base = filepath.Dir(workingDirectory)
	}
	path, err := filepath.Abs(filepath.Join(base, configured))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	return path, nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolEnv(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func csvEnv(name string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func fatal(operation string, err error) {
	slog.Error(operation, "error", err)
	os.Exit(1)
}

func ptrBool(value bool) *bool { return &value }

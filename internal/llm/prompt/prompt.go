package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
)

func GetAgentPrompt(agentName config.AgentName, provider models.ModelProvider) string {
	basePrompt := ""
	switch agentName {
	case config.AgentCoder:
		basePrompt = CoderPrompt(provider)
	case config.AgentTitle:
		basePrompt = TitlePrompt(provider)
	case config.AgentTask:
		basePrompt = TaskPrompt(provider)
	case config.AgentSummarizer:
		basePrompt = SummarizerPrompt(provider)
	case config.AgentReviewer:
		basePrompt = ReviewerPrompt(provider)
	case config.AgentTester:
		basePrompt = TesterPrompt(provider)
	case config.AgentDebugger:
		basePrompt = DebuggerPrompt(provider)
	case config.AgentArchitect:
		basePrompt = ArchitectPrompt(provider)
	default:
		basePrompt = "You are a helpful assistant"
	}

	if agentName == config.AgentCoder || agentName == config.AgentTask ||
		agentName == config.AgentReviewer || agentName == config.AgentTester ||
		agentName == config.AgentDebugger || agentName == config.AgentArchitect {
		// Add context from project-specific instruction files if they exist
		contextContent := getContextFromPaths()
		logging.Debug("Context content", "Context", contextContent)
		if contextContent != "" {
			basePrompt = fmt.Sprintf("%s\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n%s", basePrompt, contextContent)
		}

		// Add persistent project memories if available
		workDir := config.WorkingDirectory()
		if memSummary := getMemorySummary(workDir); memSummary != "" {
			basePrompt = fmt.Sprintf("%s\n\n# Persistent Project Memories\n%s", basePrompt, memSummary)
		}
	}
	return basePrompt
}

var (
	memoryProviderMu sync.RWMutex
	memoryProvider   func(projectPath string) string
)

// SetMemoryProvider sets the callback function to fetch persistent project memories.
func SetMemoryProvider(p func(projectPath string) string) {
	memoryProviderMu.Lock()
	defer memoryProviderMu.Unlock()
	memoryProvider = p
}

func getMemorySummary(projectPath string) string {
	memoryProviderMu.RLock()
	defer memoryProviderMu.RUnlock()
	if memoryProvider != nil {
		return memoryProvider(projectPath)
	}
	return ""
}

func getContextFromPaths() string {
	cfg := config.Get()
	if cfg == nil {
		return ""
	}

	return processContextPaths(cfg.WorkingDir, cfg.ContextPaths)
}

func processContextPaths(workDir string, paths []string) string {
	var (
		wg       sync.WaitGroup
		resultCh = make(chan string)
	)

	// Track processed files to avoid duplicates
	processedFiles := make(map[string]bool)
	var processedMutex sync.Mutex

	for _, path := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()

			if strings.HasSuffix(p, "/") {
				filepath.WalkDir(filepath.Join(workDir, p), func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !d.IsDir() {
						// Check if we've already processed this file (case-insensitive)
						processedMutex.Lock()
						lowerPath := strings.ToLower(path)
						if !processedFiles[lowerPath] {
							processedFiles[lowerPath] = true
							processedMutex.Unlock()

							if result := processFile(path); result != "" {
								resultCh <- result
							}
						} else {
							processedMutex.Unlock()
						}
					}
					return nil
				})
			} else {
				fullPath := filepath.Join(workDir, p)

				// Check if we've already processed this file (case-insensitive)
				processedMutex.Lock()
				lowerPath := strings.ToLower(fullPath)
				if !processedFiles[lowerPath] {
					processedFiles[lowerPath] = true
					processedMutex.Unlock()

					result := processFile(fullPath)
					if result != "" {
						resultCh <- result
					}
				} else {
					processedMutex.Unlock()
				}
			}
		}(path)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	results := make([]string, 0)
	for result := range resultCh {
		results = append(results, result)
	}

	return strings.Join(results, "\n")
}

func processFile(filePath string) string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	return "# From:" + filePath + "\n" + string(content)
}

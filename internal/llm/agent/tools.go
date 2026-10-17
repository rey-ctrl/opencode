package agent

import (
	"context"

	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

func CoderAgentTools(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	memoryService ...memory.Service,
) []tools.BaseTool {
	return CoderAgentToolsWithBroker(permissions, sessions, messages, history, lspClients, nil, memoryService...)
}

// CoderAgentToolsWithBroker creates the coder agent tool set with an optional parent broker
// for subagent progress streaming.
func CoderAgentToolsWithBroker(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	broker *pubsub.Broker[AgentEvent],
	memoryService ...memory.Service,
) []tools.BaseTool {
	ctx := context.Background()
	otherTools := GetMcpTools(ctx, permissions)
	if len(lspClients) > 0 {
		otherTools = append(otherTools, tools.NewDiagnosticsTool(lspClients))
		otherTools = append(otherTools, tools.NewFindSymbolTool(lspClients))
	}

	baseTools := []tools.BaseTool{
		tools.NewBashTool(permissions),
		tools.NewEditTool(lspClients, permissions, history),
		tools.NewFetchTool(permissions),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
		tools.NewPatchTool(lspClients, permissions, history),
		tools.NewWriteTool(lspClients, permissions, history),
		tools.NewMultiEditTool(lspClients, permissions, history),
		tools.NewTestRunnerTool(permissions),
		NewAgentTool(sessions, messages, lspClients, broker),
	}

	if len(memoryService) > 0 && memoryService[0] != nil {
		baseTools = append(baseTools,
			tools.NewRememberTool(memoryService[0]),
			tools.NewRecallTool(memoryService[0]),
		)
	}

	return append(baseTools, otherTools...)
}

func TaskAgentTools(lspClients map[string]*lsp.Client) []tools.BaseTool {
	taskTools := []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
	}
	if len(lspClients) > 0 {
		taskTools = append(taskTools, tools.NewFindSymbolTool(lspClients))
	}
	return taskTools
}

// ReviewerAgentTools provides a safe read-only tool suite for code quality,
// security analysis, and architectural review. No mutating tools are included.
func ReviewerAgentTools(lspClients map[string]*lsp.Client, memoryService ...memory.Service) []tools.BaseTool {
	reviewerTools := []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
	}
	if len(lspClients) > 0 {
		reviewerTools = append(reviewerTools, tools.NewDiagnosticsTool(lspClients))
		reviewerTools = append(reviewerTools, tools.NewFindSymbolTool(lspClients))
	}
	if len(memoryService) > 0 && memoryService[0] != nil {
		reviewerTools = append(reviewerTools, tools.NewRecallTool(memoryService[0]))
	}
	return reviewerTools
}

// TesterAgentTools provides test-running capability via bash alongside file inspection,
// but excludes direct file-modification tools.
func TesterAgentTools(permissions permission.Service, lspClients map[string]*lsp.Client, memoryService ...memory.Service) []tools.BaseTool {
	testerTools := []tools.BaseTool{
		tools.NewBashTool(permissions),
		tools.NewTestRunnerTool(permissions),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewViewTool(lspClients),
	}
	if len(lspClients) > 0 {
		testerTools = append(testerTools, tools.NewDiagnosticsTool(lspClients))
		testerTools = append(testerTools, tools.NewFindSymbolTool(lspClients))
	}
	if len(memoryService) > 0 && memoryService[0] != nil {
		testerTools = append(testerTools, tools.NewRecallTool(memoryService[0]))
	}
	return testerTools
}

// DebuggerAgentTools provides diagnostics, symbol lookups, bash execution, and file inspection
// tailored for root cause investigation.
func DebuggerAgentTools(permissions permission.Service, lspClients map[string]*lsp.Client, memoryService ...memory.Service) []tools.BaseTool {
	debuggerTools := []tools.BaseTool{
		tools.NewBashTool(permissions),
		tools.NewTestRunnerTool(permissions),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewViewTool(lspClients),
	}
	if len(lspClients) > 0 {
		debuggerTools = append(debuggerTools, tools.NewDiagnosticsTool(lspClients))
		debuggerTools = append(debuggerTools, tools.NewFindSymbolTool(lspClients))
	}
	if len(memoryService) > 0 && memoryService[0] != nil {
		debuggerTools = append(debuggerTools, tools.NewRecallTool(memoryService[0]))
	}
	return debuggerTools
}

// ArchitectAgentTools provides read-only exploration and task delegation to subordinate agents.
func ArchitectAgentTools(
	sessions session.Service,
	messages message.Service,
	lspClients map[string]*lsp.Client,
	memoryService ...memory.Service,
) []tools.BaseTool {
	return ArchitectAgentToolsWithBroker(sessions, messages, lspClients, nil, memoryService...)
}

// ArchitectAgentToolsWithBroker creates architect agent tools with an optional parent broker.
func ArchitectAgentToolsWithBroker(
	sessions session.Service,
	messages message.Service,
	lspClients map[string]*lsp.Client,
	broker *pubsub.Broker[AgentEvent],
	memoryService ...memory.Service,
) []tools.BaseTool {
	architectTools := []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
		NewAgentTool(sessions, messages, lspClients, broker),
	}
	if len(lspClients) > 0 {
		architectTools = append(architectTools, tools.NewDiagnosticsTool(lspClients))
		architectTools = append(architectTools, tools.NewFindSymbolTool(lspClients))
	}
	if len(memoryService) > 0 && memoryService[0] != nil {
		architectTools = append(architectTools,
			tools.NewRememberTool(memoryService[0]),
			tools.NewRecallTool(memoryService[0]),
		)
	}
	return architectTools
}

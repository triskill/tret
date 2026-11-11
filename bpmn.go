// Package tret embeds the agent-loop process definition shared by
// cmd/server and any tooling that needs the BPMN XML without
// depending on the current working directory.
package tret

import _ "embed"

// AgentLoopBPMN is the BPMN 2.0 XML of the agent_loop process,
// kept in sync with bpmn/agent_loop.bpmn at the repository root.
//
//go:embed bpmn/agent_loop.bpmn
var AgentLoopBPMN []byte

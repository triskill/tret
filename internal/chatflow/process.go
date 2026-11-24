// Package chatflow embeds the engine-owned interactive chat loop.
package chatflow

import _ "embed"

// BPMN pauses at user tasks for terminal input, approval, and observation.
// Model and shell tasks use the generic toolkit contracts.
//
//go:embed chat.bpmn
var BPMN []byte

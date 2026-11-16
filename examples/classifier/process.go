// Package classifier embeds the example process for deployment and tests.
package classifier

import _ "embed"

// BPMN classifies one request, then routes to an application-owned worker.
//
//go:embed classifier.bpmn
var BPMN []byte

// Package collector renders PromQL templates, pulls history in budgeted segments and reports data quality.
package collector

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cylonchau/kubeforecast/pkg/model"
)

// labelValue accepts Kubernetes object names and service names. The pattern excludes quotes, backslashes and
// braces, so a value can never close the PromQL string it is placed in.
var labelValue = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$`)

// Render fills the {{name}} variables of a PromQL template. Every variable the template uses must be given
// and valid; values are never escaped, they are rejected when they could change the query.
func Render(template string, vars map[string]string) (string, error) {
	var firstErr error
	rendered := placeholder.ReplaceAllStringFunc(template, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		value, ok := vars[name]
		switch {
		case !ok || value == "":
			if firstErr == nil {
				firstErr = fmt.Errorf("template variable %q has no value", name)
			}
		case name == "range":
			if !rangeValue.MatchString(value) && firstErr == nil {
				firstErr = fmt.Errorf("invalid range %q", value)
			}
		case !labelValue.MatchString(value):
			if firstErr == nil {
				firstErr = fmt.Errorf("invalid value for %q", name)
			}
		}
		return value
	})
	if firstErr != nil {
		return "", firstErr
	}
	return rendered, nil
}

var (
	placeholder = regexp.MustCompile(`\{\{\s*[^{}\s]*\s*\}\}`)
	rangeValue  = regexp.MustCompile(`^[1-9][0-9]{0,5}[smhdw]$`)
)

// RateWindow is the lookback injected as {{range}}: at least four steps and never below one minute, in whole
// seconds so it is always a valid PromQL duration.
func RateWindow(step time.Duration) string {
	window := 4 * step
	if window < time.Minute {
		window = time.Minute
	}
	return fmt.Sprintf("%ds", int(window.Seconds()))
}

// Target identifies the workload a module observes.
type Target struct {
	Namespace string
	Workload  string
	Container string
	Service   string
}

// Variables returns the template variables for a target and step. Pod-level mappings cannot be collected for a
// workload target, since no single pod represents it.
func (t Target) Variables(step time.Duration) map[string]string {
	vars := map[string]string{"range": RateWindow(step)}
	for name, value := range map[string]string{"namespace": t.Namespace, "workload": t.Workload, "container": t.Container, "service": t.Service} {
		if value != "" {
			vars[name] = value
		}
	}
	return vars
}

// RenderMapping renders a metric mapping for a target, rejecting mappings that need a pod.
func RenderMapping(m *model.MetricMapping, t Target) (string, error) {
	for _, name := range model.TemplateVariables(m.Query) {
		if name == "pod" {
			return "", fmt.Errorf("mapping %q is pod level and cannot be collected for a workload target", m.Name)
		}
	}
	return Render(m.Query, t.Variables(time.Duration(m.StepSeconds)*time.Second))
}

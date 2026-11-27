package assistant

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Every figure in an answer has to come from a tool (P11 C6). The model is told so, and this is the check that does not rely on its obedience: the figures of the answer that
// carry a unit or a decimal point are looked for among the numbers the tools returned in the same question, and the ones that are not there are named under the answer, so the
// reader knows which figures were not measured. Nothing is held back and nothing is rewritten: the answer is the model's, the note is the platform's.
//
// A figure is matched the way a person writes it: the platform returns bytes and cores, the answer says GiB and millicores, a percentage, a rounding. So a figure is kept as it is
// grounded when some number of a tool, read in the unit of the figure and rounded to the digits the answer shows, is the same (or within a hundredth of its size, for a figure
// the answer rounded further than it shows). A bare whole number is not checked: "3 modules" is a count the answer makes of a list it was given, and a date, a version or the
// name of a percentile (P95) is not a measurement.

// a number with an optional unit right after it
var figureRe = regexp.MustCompile(`(?i)(^|[^\w.\-:/])(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?(?:\s?(gib|mib|kib|gb|mb|kb|%|millicores?|milli|cores?|核|毫核|vcpu|m\b|c\b|x\b|×|倍))?`)

// unit factors: how many of the tool's own unit one of the answer's makes (bytes, cores, a ratio)
var unitFactors = map[string][]float64{
	"gib": {1 << 30}, "mib": {1 << 20}, "kib": {1 << 10},
	"gb": {1e9, 1 << 30}, "mb": {1e6, 1 << 20}, "kb": {1e3, 1 << 10},
	"%":         {0.01, 1},
	"millicore": {1e-3, 1}, "millicores": {1e-3, 1}, "milli": {1e-3, 1}, "毫核": {1e-3, 1}, "m": {1e-3, 1},
	"core": {1}, "cores": {1}, "核": {1}, "vcpu": {1}, "c": {1},
	"x": {1}, "×": {1}, "倍": {1},
	"": {1},
}

// Figure is a number of an answer that is looked for in the tool results.
type Figure struct {
	Text  string
	Value float64
	Unit  string
	// Digits is how many digits after the point the answer shows.
	Digits int
}

// FiguresOf lists the figures of an answer that are worth checking: with a unit or a decimal point. Code, links and the names that carry digits (P95, v1.2.3) are left out.
func FiguresOf(answer string) []Figure {
	text := stripCode(answer)
	var out []Figure
	for _, m := range figureRe.FindAllStringSubmatchIndex(text, -1) {
		whole := strings.ReplaceAll(text[m[4]:m[5]], ",", "")
		frac := ""
		if m[6] >= 0 {
			frac = text[m[6]:m[7]]
		}
		unit := ""
		if m[8] >= 0 {
			unit = strings.ToLower(text[m[8]:m[9]])
		}
		if frac == "" && unit == "" {
			continue // a bare whole number: a count, a year, a day
		}
		// the letter before it makes it part of a name (P95, v2, sha1), a digit and a point after it a version or an address
		if m[4] > 0 {
			if prev := text[m[4]-1]; prev == 'P' || prev == 'p' || prev == 'v' || prev == 'V' {
				continue
			}
		}
		if rest := text[m[1]:]; strings.HasPrefix(rest, ".") && len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9' {
			continue
		}
		v, err := strconv.ParseFloat(whole+frac, 64)
		if err != nil {
			continue
		}
		out = append(out, Figure{Text: strings.TrimSpace(text[m[4]:m[1]]), Value: v, Unit: unit, Digits: max(len(frac)-1, 0)})
	}
	return out
}

// stripCode drops fenced and inline code and links: what is quoted there is not a figure of the answer.
var codeRe = regexp.MustCompile("(?s)```.*?```|`[^`]*`|\\]\\([^)]*\\)|https?://\\S+")

func stripCode(s string) string { return codeRe.ReplaceAllString(s, " ") }

// NumbersOf reads every number of the JSON the tools returned.
func NumbersOf(evidence []string) []float64 {
	var out []float64
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case float64:
			out = append(out, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case string:
			// a figure told in words ("4.45 cores") is evidence too
			for _, f := range FiguresOf(x) {
				out = append(out, f.Value)
			}
		}
	}
	for _, raw := range evidence {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			// cut short or not JSON: the numbers in the text are still what the tool said
			for _, f := range figureRe.FindAllStringSubmatch(raw, -1) {
				if n, err := strconv.ParseFloat(strings.ReplaceAll(f[2], ",", "")+f[3], 64); err == nil {
					out = append(out, n)
				}
			}
			continue
		}
		walk(v)
	}
	return out
}

// grounded says whether a figure is among the numbers of the tools, read in its own unit and rounded as the answer rounded it.
func grounded(f Figure, numbers []float64) bool {
	factors, ok := unitFactors[f.Unit]
	if !ok {
		factors = []float64{1}
	}
	half := 0.5 * math.Pow(10, -float64(f.Digits))
	for _, factor := range factors {
		for _, n := range numbers {
			shown := n / factor
			if math.Abs(shown-f.Value) <= half+1e-9 || math.Abs(shown-f.Value) <= math.Abs(f.Value)*0.01 {
				return true
			}
		}
	}
	// a percentage can be the share of two numbers of the tool, and "x2" a ratio of two: the tool's own ratios are in its result, and a figure that is only derivable is not grounded
	return false
}

// UngroundedFigures are the figures of the answer that no number of the tools matches. With no evidence (no tool was called) nothing is said: an answer without a read has
// no tool to be checked against, and says what it knows in words.
func UngroundedFigures(answer string, evidence []string) []string {
	if len(evidence) == 0 {
		return nil
	}
	numbers := NumbersOf(evidence)
	var out []string
	seen := map[string]bool{}
	for _, f := range FiguresOf(answer) {
		if grounded(f, numbers) || seen[f.Text] {
			continue
		}
		seen[f.Text] = true
		out = append(out, f.Text)
	}
	return out
}

// maxUngrounded is how many figures the note names.
const maxUngrounded = 8

// ungroundedNote is the line under an answer that names the figures no tool gave.
func ungroundedNote(figures []string, lang string) string {
	if len(figures) == 0 {
		return ""
	}
	if len(figures) > maxUngrounded {
		figures = append(figures[:maxUngrounded:maxUngrounded], "…")
	}
	list := strings.Join(figures, "、")
	if lang == "en" {
		return "\n\n> Not measured: the figures " + strings.ReplaceAll(list, "、", ", ") + " were not in what the tools returned, so they are not the platform's figures. Check them before relying on them."
	}
	return "\n\n> 未经测量：数字 " + list + " 不在工具返回的结果里，不是平台算出的数字，使用前请先核对。"
}

// verifyFigures names under the answer the figures that no tool of the question returned (P11 C6), and lists them in the reply for the screens that show them apart.
func verifyFigures(reply *Reply, t *trail, lang string) {
	if reply == nil || t == nil {
		return
	}
	if missing := UngroundedFigures(reply.Content, t.seen()); len(missing) > 0 {
		reply.Unverified = missing
		reply.Content += ungroundedNote(missing, lang)
	}
}

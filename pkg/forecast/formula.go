package forecast

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The formula implementation lets an administrator add an algorithm without Go code: a parameter schema (names, types,
// defaults, ranges, units, descriptions) and a formula over the history. The formula is parsed once and evaluated by
// the interpreter below. It is not a general language: there are no loops, no assignments, no calls out of the
// function table, and every evaluation is counted against a budget, so a formula can neither escape nor run away.
//
// What a formula sees:
//
//	x            the history, oldest first (a series); inside a recursive formula the forecast steps so far are appended
//	k            how many steps ahead the value is for (1 = the next step)
//	h            the forecast horizon in steps
//	n            the length of x
//	<param>      each parameter of the schema, as a number (bool: 0 or 1)
//	c_<source>   the history of another forecast source of the module, when it has one
//
// and what it can call (S is a series, N a number):
//
//	last(S)            the newest value                         mean(S,N) sum(S,N) std(S,N) median(S,N)   over the newest N values
//	smin(S,N) smax(S,N) the smallest and the largest of the newest N
//	pct(S,N,P)         the P-th percentile (0-100) of the newest N
//	ema(S,span)        the exponential moving average at the end of S (alpha = 2/(span+1))
//	lag(S,j)           the value j steps before the newest   diff(S,j)  last(S) - lag(S,j)
//	slope(S,N)         the least-squares slope per step over the newest N
//	linfit(S,N,j)      that line extended j steps beyond the newest value
//	season(S,m,j)      the value one season (m steps) earlier at the phase of step j ahead
//	abs sqrt exp log floor ceil round pow(a,b) min(a,b) max(a,b) clip(v,lo,hi) if(cond,a,b)
//
// Operators: + - * / % ^ (power), comparisons, && || !. A comparison is 1 or 0.
const (
	maxFormulaLength = 2000
	maxFormulaNodes  = 400
	// the work one Forecast call may do, counted in evaluated nodes plus the series elements the functions visit
	formulaBudget = 400_000_000
)

type formulaType int

const (
	typeNumber formulaType = iota
	typeSeries
)

type fnSpec struct {
	args []formulaType
	call func(a []formulaValue, c *evalCtx) (float64, error)
}

type formulaValue struct {
	f float64
	s []float64
}

// evalCtx is what one evaluation reads and counts.
type evalCtx struct {
	vars   map[string]float64
	series map[string][]float64
	spent  int64
}

func (c *evalCtx) spend(n int) error {
	c.spent += int64(n)
	if c.spent > formulaBudget {
		return fmt.Errorf("the formula is too expensive: it did more than %d operations in one forecast", int64(formulaBudget))
	}
	return nil
}

type node interface {
	typ() formulaType
	eval(c *evalCtx) (formulaValue, error)
}

type (
	numNode struct{ v float64 }
	varNode struct{ name string }
	serNode struct{ name string }
	unNode  struct {
		op string
		a  node
	}
	binNode struct {
		op   string
		a, b node
	}
	callNode struct {
		name string
		fn   fnSpec
		args []node
	}
	ifNode struct{ cond, a, b node }
)

func (numNode) typ() formulaType  { return typeNumber }
func (varNode) typ() formulaType  { return typeNumber }
func (serNode) typ() formulaType  { return typeSeries }
func (unNode) typ() formulaType   { return typeNumber }
func (binNode) typ() formulaType  { return typeNumber }
func (callNode) typ() formulaType { return typeNumber }
func (ifNode) typ() formulaType   { return typeNumber }

func (n numNode) eval(c *evalCtx) (formulaValue, error) { return formulaValue{f: n.v}, c.spend(1) }

func (n varNode) eval(c *evalCtx) (formulaValue, error) {
	v, ok := c.vars[n.name]
	if !ok {
		return formulaValue{}, fmt.Errorf("variable %q has no value", n.name)
	}
	return formulaValue{f: v}, c.spend(1)
}

func (n serNode) eval(c *evalCtx) (formulaValue, error) {
	s, ok := c.series[n.name]
	if !ok {
		return formulaValue{}, fmt.Errorf("the series %q is not available for this forecast", n.name)
	}
	return formulaValue{s: s}, c.spend(1)
}

func (n unNode) eval(c *evalCtx) (formulaValue, error) {
	a, err := n.a.eval(c)
	if err != nil {
		return a, err
	}
	if n.op == "!" {
		return formulaValue{f: boolNum(a.f == 0)}, c.spend(1)
	}
	return formulaValue{f: -a.f}, c.spend(1)
}

func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (n binNode) eval(c *evalCtx) (formulaValue, error) {
	a, err := n.a.eval(c)
	if err != nil {
		return a, err
	}
	// && and || do not evaluate the right side when the left decides
	if n.op == "&&" && a.f == 0 {
		return formulaValue{f: 0}, nil
	}
	if n.op == "||" && a.f != 0 {
		return formulaValue{f: 1}, nil
	}
	b, err := n.b.eval(c)
	if err != nil {
		return b, err
	}
	if err := c.spend(1); err != nil {
		return formulaValue{}, err
	}
	var r float64
	switch n.op {
	case "+":
		r = a.f + b.f
	case "-":
		r = a.f - b.f
	case "*":
		r = a.f * b.f
	case "/":
		if b.f == 0 {
			return formulaValue{}, fmt.Errorf("division by zero")
		}
		r = a.f / b.f
	case "%":
		if b.f == 0 {
			return formulaValue{}, fmt.Errorf("modulo by zero")
		}
		r = math.Mod(a.f, b.f)
	case "^":
		r = math.Pow(a.f, b.f)
	case "<":
		r = boolNum(a.f < b.f)
	case "<=":
		r = boolNum(a.f <= b.f)
	case ">":
		r = boolNum(a.f > b.f)
	case ">=":
		r = boolNum(a.f >= b.f)
	case "==":
		r = boolNum(a.f == b.f)
	case "!=":
		r = boolNum(a.f != b.f)
	case "&&", "||":
		r = boolNum(b.f != 0)
	}
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return formulaValue{}, fmt.Errorf("%s gave a value that is not finite", n.op)
	}
	return formulaValue{f: r}, nil
}

func (n ifNode) eval(c *evalCtx) (formulaValue, error) {
	cond, err := n.cond.eval(c)
	if err != nil {
		return cond, err
	}
	if cond.f != 0 {
		return n.a.eval(c)
	}
	return n.b.eval(c)
}

func (n callNode) eval(c *evalCtx) (formulaValue, error) {
	args := make([]formulaValue, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(c)
		if err != nil {
			return v, err
		}
		args[i] = v
	}
	r, err := n.fn.call(args, c)
	if err != nil {
		return formulaValue{}, fmt.Errorf("%s: %w", n.name, err)
	}
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return formulaValue{}, fmt.Errorf("%s gave a value that is not finite", n.name)
	}
	return formulaValue{f: r}, c.spend(1)
}

// ---- the function table

var (
	sN  = typeSeries
	nN  = typeNumber
	fns map[string]fnSpec
)

// tail returns the newest n values of s, n clamped to the length of s.
func tail(s []float64, n float64) ([]float64, error) {
	if n < 1 || n != math.Trunc(n) {
		return nil, fmt.Errorf("the window must be a whole number of steps of at least 1")
	}
	if len(s) == 0 {
		return nil, fmt.Errorf("the series is empty")
	}
	m := int(n)
	if m > len(s) {
		m = len(s)
	}
	return s[len(s)-m:], nil
}

func emaOf(s []float64, span float64, c *evalCtx) (float64, error) {
	if span < 1 {
		return 0, fmt.Errorf("the span must be at least 1")
	}
	if len(s) == 0 {
		return 0, fmt.Errorf("the series is empty")
	}
	if err := c.spend(len(s)); err != nil {
		return 0, err
	}
	alpha := 2 / (span + 1)
	v := s[0]
	for _, x := range s[1:] {
		v = alpha*x + (1-alpha)*v
	}
	return v, nil
}

func slopeOf(w []float64) float64 {
	n := float64(len(w))
	if n < 2 {
		return 0
	}
	var sx, sy, sxy, sxx float64
	for i, y := range w {
		x := float64(i)
		sx, sy, sxy, sxx = sx+x, sy+y, sxy+x*y, sxx+x*x
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

func init() {
	window := func(f func(w []float64) float64) fnSpec {
		return fnSpec{args: []formulaType{sN, nN}, call: func(a []formulaValue, c *evalCtx) (float64, error) {
			w, err := tail(a[0].s, a[1].f)
			if err != nil {
				return 0, err
			}
			if err := c.spend(len(w)); err != nil {
				return 0, err
			}
			return f(w), nil
		}}
	}
	num1 := func(f func(float64) float64) fnSpec {
		return fnSpec{args: []formulaType{nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) { return f(a[0].f), nil }}
	}
	fns = map[string]fnSpec{
		"last": {args: []formulaType{sN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			if len(a[0].s) == 0 {
				return 0, fmt.Errorf("the series is empty")
			}
			return a[0].s[len(a[0].s)-1], nil
		}},
		"mean": window(func(w []float64) float64 {
			var s float64
			for _, v := range w {
				s += v
			}
			return s / float64(len(w))
		}),
		"sum": window(func(w []float64) float64 {
			var s float64
			for _, v := range w {
				s += v
			}
			return s
		}),
		"std": window(func(w []float64) float64 {
			var m float64
			for _, v := range w {
				m += v
			}
			m /= float64(len(w))
			var s float64
			for _, v := range w {
				s += (v - m) * (v - m)
			}
			return math.Sqrt(s / float64(len(w)))
		}),
		"median": window(func(w []float64) float64 {
			cp := append([]float64(nil), w...)
			sort.Float64s(cp)
			if len(cp)%2 == 1 {
				return cp[len(cp)/2]
			}
			return (cp[len(cp)/2-1] + cp[len(cp)/2]) / 2
		}),
		"smin": window(func(w []float64) float64 {
			m := w[0]
			for _, v := range w {
				m = math.Min(m, v)
			}
			return m
		}),
		"smax": window(func(w []float64) float64 {
			m := w[0]
			for _, v := range w {
				m = math.Max(m, v)
			}
			return m
		}),
		"slope": window(slopeOf),
		"pct": {args: []formulaType{sN, nN, nN}, call: func(a []formulaValue, c *evalCtx) (float64, error) {
			w, err := tail(a[0].s, a[1].f)
			if err != nil {
				return 0, err
			}
			if a[2].f < 0 || a[2].f > 100 {
				return 0, fmt.Errorf("the percentile must be between 0 and 100")
			}
			if err := c.spend(len(w) * 4); err != nil {
				return 0, err
			}
			cp := append([]float64(nil), w...)
			sort.Float64s(cp)
			pos := a[2].f / 100 * float64(len(cp)-1)
			lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
			return cp[lo] + (cp[hi]-cp[lo])*(pos-float64(lo)), nil
		}},
		"ema": {args: []formulaType{sN, nN}, call: func(a []formulaValue, c *evalCtx) (float64, error) { return emaOf(a[0].s, a[1].f, c) }},
		"lag": {args: []formulaType{sN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			j := a[1].f
			if j < 0 || j != math.Trunc(j) || int(j) >= len(a[0].s) {
				return 0, fmt.Errorf("the lag must be a whole number between 0 and %d", len(a[0].s)-1)
			}
			return a[0].s[len(a[0].s)-1-int(j)], nil
		}},
		"diff": {args: []formulaType{sN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			j := a[1].f
			if j < 1 || j != math.Trunc(j) || int(j) >= len(a[0].s) {
				return 0, fmt.Errorf("the distance must be a whole number between 1 and %d", len(a[0].s)-1)
			}
			s := a[0].s
			return s[len(s)-1] - s[len(s)-1-int(j)], nil
		}},
		"linfit": {args: []formulaType{sN, nN, nN}, call: func(a []formulaValue, c *evalCtx) (float64, error) {
			w, err := tail(a[0].s, a[1].f)
			if err != nil {
				return 0, err
			}
			if err := c.spend(len(w) * 2); err != nil {
				return 0, err
			}
			b := slopeOf(w)
			var mean float64
			for _, v := range w {
				mean += v
			}
			mean /= float64(len(w))
			// the line passes through the mean at the middle of the window
			mid := float64(len(w)-1) / 2
			return mean + b*(float64(len(w)-1)-mid+a[2].f), nil
		}},
		"season": {args: []formulaType{sN, nN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			m, j := a[1].f, a[2].f
			if m < 1 || m != math.Trunc(m) || int(m) > len(a[0].s) {
				return 0, fmt.Errorf("the season must be a whole number of steps between 1 and %d", len(a[0].s))
			}
			if j < 1 || j != math.Trunc(j) {
				return 0, fmt.Errorf("the step ahead must be a whole number of at least 1")
			}
			s, season := a[0].s, int(m)
			return s[len(s)-season+(int(j)-1)%season], nil
		}},
		"abs":   num1(math.Abs),
		"floor": num1(math.Floor),
		"ceil":  num1(math.Ceil),
		"round": num1(math.Round),
		"exp":   num1(math.Exp),
		"sqrt": {args: []formulaType{nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			if a[0].f < 0 {
				return 0, fmt.Errorf("the square root of a negative number")
			}
			return math.Sqrt(a[0].f), nil
		}},
		"log": {args: []formulaType{nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			if a[0].f <= 0 {
				return 0, fmt.Errorf("the logarithm of a number that is not positive")
			}
			return math.Log(a[0].f), nil
		}},
		"pow": {args: []formulaType{nN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) { return math.Pow(a[0].f, a[1].f), nil }},
		"min": {args: []formulaType{nN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) { return math.Min(a[0].f, a[1].f), nil }},
		"max": {args: []formulaType{nN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) { return math.Max(a[0].f, a[1].f), nil }},
		"clip": {args: []formulaType{nN, nN, nN}, call: func(a []formulaValue, _ *evalCtx) (float64, error) {
			if a[1].f > a[2].f {
				return 0, fmt.Errorf("the lower bound is above the upper bound")
			}
			return math.Min(math.Max(a[0].f, a[1].f), a[2].f), nil
		}},
	}
}

// FormulaFunctions lists the functions a formula may call, with their signatures, for the editor of the pages.
func FormulaFunctions() []string {
	out := make([]string, 0, len(fns)+1)
	for name, f := range fns {
		parts := make([]string, len(f.args))
		for i, t := range f.args {
			parts[i] = map[formulaType]string{typeNumber: "N", typeSeries: "S"}[t]
		}
		out = append(out, name+"("+strings.Join(parts, ",")+")")
	}
	out = append(out, "if(C,N,N)")
	sort.Strings(out)
	return out
}

// ---- the parser

type parser struct {
	src    string
	pos    int
	params map[string]bool
	nodes  int
	// series lists the c_<source> series the formula reads
	series map[string]bool
}

type token struct {
	kind string // num, id, op, end
	text string
	num  float64
	at   int
}

func (p *parser) skip() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *parser) peek() (token, error) {
	save := p.pos
	t, err := p.next()
	p.pos = save
	return t, err
}

func (p *parser) next() (token, error) {
	p.skip()
	if p.pos >= len(p.src) {
		return token{kind: "end", at: p.pos}, nil
	}
	at := p.pos
	ch := p.src[p.pos]
	switch {
	case ch >= '0' && ch <= '9' || ch == '.':
		end := p.pos
		for end < len(p.src) && (p.src[end] >= '0' && p.src[end] <= '9' || p.src[end] == '.') {
			end++
		}
		// an exponent: 1e3, 2.5e-3
		if end < len(p.src) && (p.src[end] == 'e' || p.src[end] == 'E') {
			e := end + 1
			if e < len(p.src) && (p.src[e] == '+' || p.src[e] == '-') {
				e++
			}
			if e < len(p.src) && p.src[e] >= '0' && p.src[e] <= '9' {
				for e < len(p.src) && p.src[e] >= '0' && p.src[e] <= '9' {
					e++
				}
				end = e
			}
		}
		v, err := strconv.ParseFloat(p.src[p.pos:end], 64)
		if err != nil {
			return token{}, fmt.Errorf("position %d: %q is not a number", at+1, p.src[p.pos:end])
		}
		p.pos = end
		return token{kind: "num", num: v, text: p.src[at:end], at: at}, nil
	case ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z':
		end := p.pos
		for end < len(p.src) && (p.src[end] == '_' || p.src[end] >= 'a' && p.src[end] <= 'z' || p.src[end] >= 'A' && p.src[end] <= 'Z' || p.src[end] >= '0' && p.src[end] <= '9') {
			end++
		}
		p.pos = end
		return token{kind: "id", text: p.src[at:end], at: at}, nil
	}
	for _, op := range []string{"&&", "||", "<=", ">=", "==", "!=", "+", "-", "*", "/", "%", "^", "<", ">", "!", "(", ")", ","} {
		if strings.HasPrefix(p.src[p.pos:], op) {
			p.pos += len(op)
			return token{kind: "op", text: op, at: at}, nil
		}
	}
	return token{}, fmt.Errorf("position %d: unexpected character %q", at+1, string(ch))
}

func (p *parser) fail(t token, format string, args ...any) error {
	return fmt.Errorf("position %d: %s", t.at+1, fmt.Sprintf(format, args...))
}

func (p *parser) count() error {
	p.nodes++
	if p.nodes > maxFormulaNodes {
		return fmt.Errorf("the formula is too long: at most %d terms", maxFormulaNodes)
	}
	return nil
}

func (p *parser) accept(ops ...string) (string, bool, error) {
	t, err := p.peek()
	if err != nil {
		return "", false, err
	}
	if t.kind == "op" {
		for _, op := range ops {
			if t.text == op {
				_, _ = p.next()
				return op, true, nil
			}
		}
	}
	return "", false, nil
}

func (p *parser) number(n node, t token) error {
	if n.typ() != typeNumber {
		return p.fail(t, "a series is not a number here: reduce it with a function such as last(x) or mean(x, 12)")
	}
	return nil
}

func (p *parser) level(next func() (node, error), ops ...string) (node, error) {
	left, err := next()
	if err != nil {
		return nil, err
	}
	for {
		t, _ := p.peek()
		op, ok, err := p.accept(ops...)
		if err != nil {
			return nil, err
		}
		if !ok {
			return left, nil
		}
		if err := p.number(left, t); err != nil {
			return nil, err
		}
		right, err := next()
		if err != nil {
			return nil, err
		}
		if err := p.number(right, t); err != nil {
			return nil, err
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		left = binNode{op: op, a: left, b: right}
	}
}

func (p *parser) parseOr() (node, error)  { return p.level(p.parseAnd, "||") }
func (p *parser) parseAnd() (node, error) { return p.level(p.parseCmp, "&&") }
func (p *parser) parseCmp() (node, error) {
	return p.level(p.parseAdd, "<=", ">=", "==", "!=", "<", ">")
}
func (p *parser) parseAdd() (node, error) { return p.level(p.parseMul, "+", "-") }
func (p *parser) parseMul() (node, error) { return p.level(p.parseUnary, "*", "/", "%") }

func (p *parser) parseUnary() (node, error) {
	t, _ := p.peek()
	if op, ok, err := p.accept("-", "!"); err != nil {
		return nil, err
	} else if ok {
		a, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if err := p.number(a, t); err != nil {
			return nil, err
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		return unNode{op: op, a: a}, nil
	}
	base, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if _, ok, err := p.accept("^"); err != nil {
		return nil, err
	} else if ok {
		if err := p.number(base, t); err != nil {
			return nil, err
		}
		exp, err := p.parseUnary() // right associative
		if err != nil {
			return nil, err
		}
		if err := p.number(exp, t); err != nil {
			return nil, err
		}
		if err := p.count(); err != nil {
			return nil, err
		}
		return binNode{op: "^", a: base, b: exp}, nil
	}
	return base, nil
}

var builtinVars = map[string]bool{"k": true, "h": true, "n": true}

func (p *parser) parsePrimary() (node, error) {
	t, err := p.next()
	if err != nil {
		return nil, err
	}
	if err := p.count(); err != nil {
		return nil, err
	}
	switch t.kind {
	case "num":
		return numNode{v: t.num}, nil
	case "op":
		if t.text == "(" {
			e, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if _, ok, err := p.accept(")"); err != nil {
				return nil, err
			} else if !ok {
				return nil, p.fail(t, "this parenthesis is never closed")
			}
			return e, nil
		}
		return nil, p.fail(t, "unexpected %q", t.text)
	case "id":
		if _, ok, err := p.accept("("); err != nil {
			return nil, err
		} else if ok {
			return p.parseCall(t)
		}
		switch {
		case t.text == "x":
			return serNode{name: "x"}, nil
		case strings.HasPrefix(t.text, "c_") && len(t.text) > 2:
			p.series[t.text] = true
			return serNode{name: t.text}, nil
		case builtinVars[t.text] || p.params[t.text]:
			return varNode{name: t.text}, nil
		}
		return nil, p.fail(t, "%q is not a variable: use x, k, h, n, c_<source> or one of the parameters", t.text)
	}
	return nil, p.fail(t, "the formula ends where a value was expected")
}

func (p *parser) parseCall(name token) (node, error) {
	var args []node
	var firsts []token
	if _, closed, err := p.accept(")"); err != nil {
		return nil, err
	} else if !closed {
		for {
			t, _ := p.peek()
			a, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			firsts = append(firsts, t)
			if _, more, err := p.accept(","); err != nil {
				return nil, err
			} else if more {
				continue
			}
			if _, ok, err := p.accept(")"); err != nil {
				return nil, err
			} else if !ok {
				return nil, p.fail(name, "the call of %s is never closed", name.text)
			}
			break
		}
	}
	if name.text == "if" {
		if len(args) != 3 {
			return nil, p.fail(name, "if takes 3 arguments: if(condition, then, else)")
		}
		for i, a := range args {
			if err := p.number(a, firsts[i]); err != nil {
				return nil, err
			}
		}
		return ifNode{cond: args[0], a: args[1], b: args[2]}, nil
	}
	fn, ok := fns[name.text]
	if !ok {
		return nil, p.fail(name, "unknown function %q", name.text)
	}
	if len(args) != len(fn.args) {
		return nil, p.fail(name, "%s takes %d argument(s), not %d", name.text, len(fn.args), len(args))
	}
	for i, a := range args {
		if a.typ() != fn.args[i] {
			want := map[formulaType]string{typeNumber: "a number", typeSeries: "a series (x or c_<source>)"}[fn.args[i]]
			return nil, p.fail(firsts[i], "argument %d of %s must be %s", i+1, name.text, want)
		}
	}
	return callNode{name: name.text, fn: fn, args: args}, nil
}

// FormulaSpec is the definition of a formula algorithm: the parameters its author declared and the formula over them.
type FormulaSpec struct {
	Expr   string      `json:"formula"`
	Params []ParamSpec `json:"param_schema"`
	// MinSamples is the shortest history the formula accepts.
	MinSamples int `json:"min_samples"`
	// Recursive appends each forecast step to x before the next one is computed (the way a smoothing or an autoregression
	// rolls forward); otherwise every step is computed from the observed history alone, using k for the distance.
	Recursive bool `json:"recursive"`
}

// Formula is a compiled formula algorithm.
type Formula struct {
	spec   FormulaSpec
	root   node
	params map[string]float64
	series []string
}

// ParseFormula checks a formula against its parameter schema and compiles it.
func ParseFormula(spec FormulaSpec) (*Formula, error) {
	expr := strings.TrimSpace(spec.Expr)
	if expr == "" {
		return nil, fmt.Errorf("the formula is empty")
	}
	if len(expr) > maxFormulaLength {
		return nil, fmt.Errorf("the formula is longer than %d characters", maxFormulaLength)
	}
	if err := ValidateSpecs(spec.Params); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, ps := range spec.Params {
		if ps.Type == "string" {
			return nil, fmt.Errorf("parameter %q is a string: a formula only reads numbers and booleans", ps.Name)
		}
		if builtinVars[ps.Name] || ps.Name == "x" || ps.Name == "if" {
			return nil, fmt.Errorf("parameter %q has the name of a variable of the formula", ps.Name)
		}
		if _, isFn := fns[ps.Name]; isFn {
			return nil, fmt.Errorf("parameter %q has the name of a function of the formula", ps.Name)
		}
		if strings.HasPrefix(ps.Name, "c_") {
			return nil, fmt.Errorf("parameter %q starts with c_, which is for the series of other sources", ps.Name)
		}
		names[ps.Name] = true
	}
	if spec.MinSamples < 1 || spec.MinSamples > 100000 {
		return nil, fmt.Errorf("min_samples must be between 1 and 100000")
	}
	p := &parser{src: expr, params: names, series: map[string]bool{}}
	root, err := p.parseOr()
	if err != nil {
		return nil, fmt.Errorf("formula: %w", err)
	}
	if t, err := p.next(); err != nil {
		return nil, fmt.Errorf("formula: %w", err)
	} else if t.kind != "end" {
		return nil, fmt.Errorf("formula: position %d: unexpected %q after the end of the formula", t.at+1, t.text)
	}
	if root.typ() != typeNumber {
		return nil, fmt.Errorf("formula: the result is a series: reduce it with a function such as last(x)")
	}
	f := &Formula{spec: spec, root: root}
	for name := range p.series {
		f.series = append(f.series, name)
	}
	sort.Strings(f.series)
	return f, nil
}

// With binds the (already validated, complete) parameter values and returns the model.
func (f *Formula) With(values map[string]any) (Model, error) {
	bound := &Formula{spec: f.spec, root: f.root, series: f.series, params: map[string]float64{}}
	for _, ps := range f.spec.Params {
		v, ok := values[ps.Name]
		if !ok {
			return nil, fmt.Errorf("parameter %q has no value", ps.Name)
		}
		switch t := v.(type) {
		case bool:
			bound.params[ps.Name] = boolNum(t)
		case int:
			bound.params[ps.Name] = float64(t)
		case float64:
			bound.params[ps.Name] = t
		default:
			return nil, fmt.Errorf("parameter %q has a value of type %T", ps.Name, v)
		}
	}
	return bound, nil
}

// Series lists the other sources (c_<key>) the formula reads.
func (f *Formula) Series() []string { return append([]string(nil), f.series...) }

func (f *Formula) ID() string      { return "formula" }
func (f *Formula) MinSamples() int { return f.spec.MinSamples }

// Forecast evaluates the formula once per step ahead. The result of a step is clamped at zero (a resource is never
// negative) and has to be finite.
func (f *Formula) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := checkInput(f, in, horizon); err != nil {
		return nil, err
	}
	c := &evalCtx{vars: map[string]float64{"h": float64(horizon)}, series: map[string][]float64{}}
	for k, v := range f.params {
		c.vars[k] = v
	}
	for _, name := range f.series {
		key := strings.TrimPrefix(name, "c_")
		s, ok := in.Covariates[key]
		if !ok {
			return nil, fmt.Errorf("the formula reads %s, but the source %q has no history on the grid of this forecast", name, key)
		}
		c.series[name] = s
	}
	hist := append(make([]float64, 0, len(in.Values)+horizon), in.Values...)
	out := make([]float64, horizon)
	for i := 0; i < horizon; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.vars["k"], c.vars["n"] = float64(i+1), float64(len(hist))
		x := in.Values
		if f.spec.Recursive {
			x = hist
		}
		c.series["x"] = x
		v, err := f.root.eval(c)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		if v.f < 0 {
			v.f = 0
		}
		out[i] = v.f
		if f.spec.Recursive {
			hist = append(hist, v.f)
		}
	}
	return out, nil
}

// DefaultParamValues returns a value for every parameter of a schema (the default, else the minimum, else 1), used to
// try a formula out before it is saved.
func DefaultParamValues(specs []ParamSpec) map[string]any {
	out := map[string]any{}
	for _, ps := range specs {
		switch {
		case ps.Default != nil:
			out[ps.Name] = ps.Default
		case ps.Type == "bool":
			out[ps.Name] = false
		case ps.Min != nil && ps.Type == "int":
			out[ps.Name] = int(*ps.Min)
		case ps.Min != nil:
			out[ps.Name] = *ps.Min
		case ps.Type == "int":
			out[ps.Name] = 1
		default:
			out[ps.Name] = 1.0
		}
	}
	return out
}

// TryFormula compiles a formula and forecasts a synthetic daily series with it, so a formula that cannot run (a bad
// reference, a division by zero on ordinary data, a result that is not finite) is refused when it is saved, not on the
// first night it runs. It returns the forecast, which the pages show as a preview.
func TryFormula(spec FormulaSpec, values map[string]any, horizon int) ([]float64, []float64, error) {
	f, err := ParseFormula(spec)
	if err != nil {
		return nil, nil, err
	}
	if values == nil {
		values = DefaultParamValues(spec.Params)
	}
	normalized, err := NormalizeSpecs(spec.Params, values, "the formula")
	if err != nil {
		return nil, nil, err
	}
	model, err := f.With(normalized)
	if err != nil {
		return nil, nil, err
	}
	n := spec.MinSamples + 200
	if n < 400 {
		n = 400
	}
	hist := make([]float64, n)
	for i := range hist {
		hist[i] = 10 + 4*math.Sin(2*math.Pi*float64(i)/96) + float64(i%7)*0.1
	}
	cov := map[string][]float64{}
	for _, name := range f.series {
		cov[strings.TrimPrefix(name, "c_")] = hist
	}
	out, err := model.Forecast(context.Background(), Input{Values: hist, Step: 15 * 60 * 1e9, Covariates: cov}, horizon)
	if err != nil {
		return nil, nil, err
	}
	return hist[len(hist)-96:], out, nil
}

func registerFormula() {
	Register(Implementation{Definition: Definition{
		ID: "formula", Category: "custom", Covariates: true, Dynamic: true,
		Description: "Formula algorithm: the parameters and the formula are written on the page. The formula reads the history (x), the step ahead (k) and its parameters and may call the functions of the platform (mean, ema, slope, pct, macd, season ...).",
		Complexity:  "O(h x length of the history) per forecast, no training, bounded by an operation budget",
		MinHistory:  "min_samples of the formula",
	}})
}

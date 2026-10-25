package forecast

import (
	"context"
	"fmt"
	"math"
	"math/rand"
)

// Recurrent is a single-layer recurrent network (Elman RNN, GRU or LSTM) written in plain Go: it reads a window of
// the last Lags steps (the target, the covariates and optionally the calendar position per step) and predicts the
// next target value, and is applied recursively for longer horizons. Inputs are standardized with the statistics of
// the history it is trained on. Training is backpropagation through time with Adam on the most recent MaxSamples
// windows; it honours the context, so a time budget set by the caller stops it. The generator is seeded, so the
// same history and parameters give the same forecast.
//
// It is deliberately small: the history of one workload is a few thousand points, and a network with a handful of
// hidden units trained for a few epochs is what that supports. It is a candidate like any other and is only
// recommended when its backtest score earns it.
type Recurrent struct {
	Cell         string // "rnn", "gru" or "lstm"
	Lags         int
	Hidden       int
	Epochs       int
	LearningRate float64
	MaxSamples   int
	Calendar     bool
	UseCovariate bool
	Seed         int64
}

func (m Recurrent) ID() string      { return m.Cell }
func (m Recurrent) MinSamples() int { return m.Lags + 30 }

func (m Recurrent) validate() error {
	switch m.Cell {
	case "rnn", "gru", "lstm":
	default:
		return fmt.Errorf("unknown cell %q", m.Cell)
	}
	if m.Lags < 2 {
		return fmt.Errorf("lags must be at least 2")
	}
	if m.Hidden < 1 || m.Hidden > 128 {
		return fmt.Errorf("hidden_size must be between 1 and 128")
	}
	if m.Epochs < 1 {
		return fmt.Errorf("epochs must be at least 1")
	}
	if m.LearningRate <= 0 || m.LearningRate > 1 {
		return fmt.Errorf("learning_rate must be in (0, 1]")
	}
	if m.MaxSamples < 16 {
		return fmt.Errorf("max_samples must be at least 16")
	}
	return nil
}

// ---- the network

type recNet struct {
	cell      string
	in, hid   int
	gates     int
	p, g      []float64 // parameters and their gradient
	am, av    []float64 // Adam moments
	wx, wh, b []int     // offsets of each gate's input weights, recurrent weights and bias
	wo, bo    int
	steps     []recStep
	adamT     int
}

type recStep struct {
	x, hPrev, cPrev []float64
	gate            [][]float64 // activations of each gate
	h, c, tc, rh    []float64
}

func newRecNet(cell string, in, hid int, rng *rand.Rand) *recNet {
	gates := map[string]int{"rnn": 1, "gru": 3, "lstm": 4}[cell]
	n := &recNet{cell: cell, in: in, hid: hid, gates: gates}
	off := 0
	for g := 0; g < gates; g++ {
		n.wx = append(n.wx, off)
		off += hid * in
		n.wh = append(n.wh, off)
		off += hid * hid
		n.b = append(n.b, off)
		off += hid
	}
	n.wo, off = off, off+hid
	n.bo, off = off, off+1
	n.p, n.g, n.am, n.av = make([]float64, off), make([]float64, off), make([]float64, off), make([]float64, off)
	for g := 0; g < gates; g++ {
		sx := math.Sqrt(1 / float64(in))
		for i := 0; i < hid*in; i++ {
			n.p[n.wx[g]+i] = rng.NormFloat64() * sx
		}
		sh := math.Sqrt(1 / float64(hid))
		for i := 0; i < hid*hid; i++ {
			n.p[n.wh[g]+i] = rng.NormFloat64() * sh
		}
	}
	if cell == "lstm" { // forget gate bias 1: remember by default
		for i := 0; i < hid; i++ {
			n.p[n.b[1]+i] = 1
		}
	}
	for i := 0; i < hid; i++ {
		n.p[n.wo+i] = rng.NormFloat64() * math.Sqrt(1/float64(hid))
	}
	return n
}

func (n *recNet) ensureSteps(length int) {
	for len(n.steps) < length {
		s := recStep{hPrev: make([]float64, n.hid), cPrev: make([]float64, n.hid), h: make([]float64, n.hid), c: make([]float64, n.hid),
			tc: make([]float64, n.hid), rh: make([]float64, n.hid)}
		for g := 0; g < n.gates; g++ {
			s.gate = append(s.gate, make([]float64, n.hid))
		}
		n.steps = append(n.steps, s)
	}
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// preact computes Wx x + Wh h + b of gate g into dst.
func (n *recNet) preact(g int, x, h, dst []float64) {
	wx, wh, b := n.p[n.wx[g]:], n.p[n.wh[g]:], n.p[n.b[g]:]
	for i := 0; i < n.hid; i++ {
		s := b[i]
		row := wx[i*n.in : (i+1)*n.in]
		for j, v := range x {
			s += row[j] * v
		}
		hrow := wh[i*n.hid : (i+1)*n.hid]
		for j, v := range h {
			s += hrow[j] * v
		}
		dst[i] = s
	}
}

// forward runs the sequence (one input vector per step) and returns the prediction of the step after it.
func (n *recNet) forward(seq [][]float64) float64 {
	n.ensureSteps(len(seq))
	h := make([]float64, n.hid)
	c := make([]float64, n.hid)
	for t, x := range seq {
		s := &n.steps[t]
		s.x = x
		copy(s.hPrev, h)
		copy(s.cPrev, c)
		switch n.cell {
		case "rnn":
			n.preact(0, x, h, s.gate[0])
			for i := range s.h {
				s.gate[0][i] = math.Tanh(s.gate[0][i])
				s.h[i] = s.gate[0][i]
			}
		case "gru":
			n.preact(0, x, h, s.gate[0]) // z
			n.preact(1, x, h, s.gate[1]) // r
			for i := range h {
				s.gate[0][i] = sigmoid(s.gate[0][i])
				s.gate[1][i] = sigmoid(s.gate[1][i])
				s.rh[i] = s.gate[1][i] * h[i]
			}
			n.preact(2, x, s.rh, s.gate[2]) // candidate, reads r*h
			for i := range h {
				s.gate[2][i] = math.Tanh(s.gate[2][i])
				s.h[i] = (1-s.gate[0][i])*s.gate[2][i] + s.gate[0][i]*h[i]
			}
		case "lstm":
			for g := 0; g < 4; g++ {
				n.preact(g, x, h, s.gate[g])
			}
			for i := range h {
				s.gate[0][i] = sigmoid(s.gate[0][i]) // input
				s.gate[1][i] = sigmoid(s.gate[1][i]) // forget
				s.gate[2][i] = sigmoid(s.gate[2][i]) // output
				s.gate[3][i] = math.Tanh(s.gate[3][i])
				s.c[i] = s.gate[1][i]*c[i] + s.gate[0][i]*s.gate[3][i]
				s.tc[i] = math.Tanh(s.c[i])
				s.h[i] = s.gate[2][i] * s.tc[i]
			}
		}
		copy(h, s.h)
		copy(c, s.c)
	}
	out := n.p[n.bo]
	for i := 0; i < n.hid; i++ {
		out += n.p[n.wo+i] * h[i]
	}
	return out
}

// addGate accumulates the gradient of the pre-activation dpre of gate g into the parameters and into dhPrev.
func (n *recNet) addGate(g int, dpre, x, hIn, dhIn []float64) {
	gwx, gwh, gb := n.g[n.wx[g]:], n.g[n.wh[g]:], n.g[n.b[g]:]
	wh := n.p[n.wh[g]:]
	for i := 0; i < n.hid; i++ {
		d := dpre[i]
		if d == 0 {
			continue
		}
		gb[i] += d
		row := gwx[i*n.in : (i+1)*n.in]
		for j, v := range x {
			row[j] += d * v
		}
		hrow := gwh[i*n.hid : (i+1)*n.hid]
		whrow := wh[i*n.hid : (i+1)*n.hid]
		for j, v := range hIn {
			hrow[j] += d * v
			dhIn[j] += d * whrow[j]
		}
	}
}

// backward accumulates the gradient of 0.5*(prediction-target)^2 into n.g; forward must have run on the sequence.
func (n *recNet) backward(length int, pred, target float64) {
	d := pred - target
	last := n.steps[length-1].h
	dh := make([]float64, n.hid)
	for i := 0; i < n.hid; i++ {
		n.g[n.wo+i] += d * last[i]
		dh[i] = d * n.p[n.wo+i]
	}
	n.g[n.bo] += d
	dc := make([]float64, n.hid)
	dhPrev := make([]float64, n.hid)
	dpre := make([][]float64, n.gates)
	for g := range dpre {
		dpre[g] = make([]float64, n.hid)
	}
	drh := make([]float64, n.hid)
	for t := length - 1; t >= 0; t-- {
		s := &n.steps[t]
		for i := range dhPrev {
			dhPrev[i] = 0
		}
		switch n.cell {
		case "rnn":
			for i := 0; i < n.hid; i++ {
				dpre[0][i] = dh[i] * (1 - s.h[i]*s.h[i])
			}
			n.addGate(0, dpre[0], s.x, s.hPrev, dhPrev)
		case "gru":
			z, r, cand := s.gate[0], s.gate[1], s.gate[2]
			for i := 0; i < n.hid; i++ {
				dpre[2][i] = dh[i] * (1 - z[i]) * (1 - cand[i]*cand[i])
				dpre[0][i] = dh[i] * (s.hPrev[i] - cand[i]) * z[i] * (1 - z[i])
				dhPrev[i] += dh[i] * z[i]
				drh[i] = 0
			}
			// the candidate reads r*h: its recurrent gradient lands on r*h, then splits into r and h
			gwh := n.g[n.wh[2]:]
			wh := n.p[n.wh[2]:]
			gwx, gb := n.g[n.wx[2]:], n.g[n.b[2]:]
			for i := 0; i < n.hid; i++ {
				dd := dpre[2][i]
				if dd == 0 {
					continue
				}
				gb[i] += dd
				for j, v := range s.x {
					gwx[i*n.in+j] += dd * v
				}
				for j := 0; j < n.hid; j++ {
					gwh[i*n.hid+j] += dd * s.rh[j]
					drh[j] += dd * wh[i*n.hid+j]
				}
			}
			for i := 0; i < n.hid; i++ {
				dpre[1][i] = drh[i] * s.hPrev[i] * r[i] * (1 - r[i])
				dhPrev[i] += drh[i] * r[i]
			}
			n.addGate(0, dpre[0], s.x, s.hPrev, dhPrev)
			n.addGate(1, dpre[1], s.x, s.hPrev, dhPrev)
		case "lstm":
			ig, fg, og, gg := s.gate[0], s.gate[1], s.gate[2], s.gate[3]
			for i := 0; i < n.hid; i++ {
				dcTotal := dc[i] + dh[i]*og[i]*(1-s.tc[i]*s.tc[i])
				dpre[2][i] = dh[i] * s.tc[i] * og[i] * (1 - og[i])
				dpre[0][i] = dcTotal * gg[i] * ig[i] * (1 - ig[i])
				dpre[1][i] = dcTotal * s.cPrev[i] * fg[i] * (1 - fg[i])
				dpre[3][i] = dcTotal * ig[i] * (1 - gg[i]*gg[i])
				dc[i] = dcTotal * fg[i]
			}
			for g := 0; g < 4; g++ {
				n.addGate(g, dpre[g], s.x, s.hPrev, dhPrev)
			}
		}
		copy(dh, dhPrev)
	}
}

func (n *recNet) zeroGrad() {
	for i := range n.g {
		n.g[i] = 0
	}
}

// adam applies the accumulated gradient, clipped to a norm of 5.
func (n *recNet) adam(lr float64) {
	var norm float64
	for _, v := range n.g {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	scale := 1.0
	if norm > 5 {
		scale = 5 / norm
	}
	n.adamT++
	b1, b2 := 0.9, 0.999
	c1 := 1 - math.Pow(b1, float64(n.adamT))
	c2 := 1 - math.Pow(b2, float64(n.adamT))
	for i := range n.p {
		g := n.g[i] * scale
		n.am[i] = b1*n.am[i] + (1-b1)*g
		n.av[i] = b2*n.av[i] + (1-b2)*g*g
		n.p[i] -= lr * (n.am[i] / c1) / (math.Sqrt(n.av[i]/c2) + 1e-8)
	}
}

// ---- the forecaster

type channelStats struct{ mean, std float64 }

func statsOf(v []float64) channelStats {
	mean := windowMean(v)
	var s float64
	for _, x := range v {
		s += (x - mean) * (x - mean)
	}
	std := math.Sqrt(s / float64(len(v)))
	if std < 1e-12 {
		std = 1
	}
	return channelStats{mean, std}
}

func (m Recurrent) Forecast(ctx context.Context, in Input, horizon int) ([]float64, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := checkInput(m, in, horizon); err != nil {
		return nil, err
	}
	n := len(in.Values)
	if n < m.Lags+16 {
		return nil, fmt.Errorf("%w: %s needs %d samples, got %d", ErrInsufficientHistory, m.Cell, m.Lags+16, n)
	}
	var covNames []string
	if m.UseCovariate && len(in.Covariates) > 0 {
		spec, err := (featureSpec{Lags: 1, CovLags: 1}).withCovariates(in)
		if err != nil {
			return nil, err
		}
		covNames = spec.covNames
	}
	target := statsOf(in.Values)
	covStats := make([]channelStats, len(covNames))
	for i, name := range covNames {
		covStats[i] = statsOf(in.Covariates[name])
	}
	width := 1 + len(covNames)
	if m.Calendar {
		width += 4
	}

	// vec builds the input of the step at history position pos; a covariate beyond the history carries its last
	// observed value, the calendar position depends only on the timestamp.
	vec := func(values []float64, pos int) []float64 {
		x := make([]float64, 0, width)
		x = append(x, (values[pos]-target.mean)/target.std)
		for i, name := range covNames {
			series := in.Covariates[name]
			p := pos
			if p > len(series)-1 {
				p = len(series) - 1
			}
			x = append(x, (series[p]-covStats[i].mean)/covStats[i].std)
		}
		if m.Calendar {
			c := calendarFeatures(in.TimeAt(pos))
			x = append(x, c[:]...)
		}
		return x
	}

	rng := rand.New(rand.NewSource(m.Seed))
	net := newRecNet(m.Cell, width, m.Hidden, rng)

	first := m.Lags
	if n-first > m.MaxSamples {
		first = n - m.MaxSamples
	}
	order := make([]int, 0, n-first)
	for t := first; t < n; t++ {
		order = append(order, t)
	}
	inputs := make([][]float64, n) // the input vector of each history position, built once
	for pos := 0; pos < n; pos++ {
		inputs[pos] = vec(in.Values, pos)
	}
	seq := make([][]float64, m.Lags)
	for epoch := 0; epoch < m.Epochs; epoch++ {
		rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		for k, t := range order {
			if k%64 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			copy(seq, inputs[t-m.Lags:t])
			net.zeroGrad()
			pred := net.forward(seq)
			net.backward(m.Lags, pred, (in.Values[t]-target.mean)/target.std)
			net.adam(m.LearningRate)
		}
	}

	// recursive forecast: the window slides over the history extended by the predictions
	values := append([]float64(nil), in.Values...)
	out := make([]float64, horizon)
	for h := 0; h < horizon; h++ {
		pos := len(values)
		for j := 0; j < m.Lags; j++ {
			p := pos - m.Lags + j
			if p < n {
				seq[j] = inputs[p]
			} else {
				seq[j] = vec(values, p)
			}
		}
		pred := net.forward(seq)*target.std + target.mean
		if math.IsNaN(pred) || math.IsInf(pred, 0) {
			return nil, fmt.Errorf("%s produced a non-finite forecast", m.Cell)
		}
		out[h] = pred
		values = append(values, pred)
	}
	return out, finite(out)
}

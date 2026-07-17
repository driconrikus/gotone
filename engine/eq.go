package engine

import "math"

const eqBands = 10

var eqFrequencies = [eqBands]float64{32, 64, 125, 250, 500, 1000, 2000, 4000, 8000, 16000}

type Biquad struct {
	a1, a2                float64
	b0, b1, b2            float64
	x1, x2, y1, y2        float64
}

func (b *Biquad) Process(x float64) float64 {
	y := b.b0*x + b.b1*b.x1 + b.b2*b.x2 - b.a1*b.y1 - b.a2*b.y2
	b.x2 = b.x1
	b.x1 = x
	b.y2 = b.y1
	b.y1 = y
	return y
}

func (b *Biquad) Reset() {
	b.x1, b.x2, b.y1, b.y2 = 0, 0, 0, 0
}

type EQ struct {
	bands  [eqBands]Biquad
	gains  [eqBands]float64
	Q      float64
	sr     float64
}

func NewEQ(sampleRate float64) *EQ {
	eq := &EQ{Q: 1.0, sr: sampleRate}
	for i, freq := range eqFrequencies {
		eq.calcBand(i, freq, 0)
	}
	return eq
}

func (eq *EQ) calcBand(i int, freq, gainDB float64) {
	w0 := 2 * math.Pi * freq / eq.sr
	alpha := math.Sin(w0) / (2 * eq.Q)

	A := math.Pow(10, gainDB/40.0)
	b0 := 1.0 + alpha*A
	b1 := -2.0 * math.Cos(w0)
	b2 := 1.0 - alpha*A
	a0 := 1.0 + alpha/A
	a1 := -2.0 * math.Cos(w0)
	a2 := 1.0 - alpha/A

	b := &eq.bands[i]
	b.b0 = b0 / a0
	b.b1 = b1 / a0
	b.b2 = b2 / a0
	b.a1 = a1 / a0
	b.a2 = a2 / a0
	eq.gains[i] = gainDB
}

func (eq *EQ) SetBand(band int, gainDB float64) {
	if band < 0 || band >= eqBands {
		return
	}
	if gainDB < -12 {
		gainDB = -12
	}
	if gainDB > 12 {
		gainDB = 12
	}
	eq.calcBand(band, eqFrequencies[band], gainDB)
}

func (eq *EQ) BandGain(band int) float64 {
	if band < 0 || band >= eqBands {
		return 0
	}
	return eq.gains[band]
}

func (eq *EQ) Bands() int {
	return eqBands
}

func (eq *EQ) Frequencies() []float64 {
	f := make([]float64, eqBands)
	copy(f, eqFrequencies[:])
	return f
}

func (eq *EQ) Reset() {
	for i := range eq.bands {
		eq.calcBand(i, eqFrequencies[i], 0)
	}
}

func (eq *EQ) ProcessSample(x float64) float64 {
	for i := range eq.bands {
		x = eq.bands[i].Process(x)
	}
	return x
}

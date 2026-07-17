package engine

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gordonklaus/portaudio"
)

type Device struct {
	Index       int
	Name        string
	MaxChannels int
}

type Stats struct {
	InputLevel  float64
	OutputLevel float64
}

type Engine struct {
	inputDevice  *portaudio.DeviceInfo
	outputDevice *portaudio.DeviceInfo

	stream *portaudio.Stream

	gain   float32
	muted  int32
	gainMu sync.RWMutex

	sampleRate     int
	framesBuf      int
	channels       int
	outputChannel  int // 1-indexed selected output channel
	outputChannels int // total output channels available

	eq *EQ

	stats   Stats
	statsMu sync.RWMutex
}

func Initialize() error {
	return portaudio.Initialize()
}

func Terminate() error {
	return portaudio.Terminate()
}

func Devices() ([]Device, []Device, error) {
	var inputs, outputs []Device

	api, err := portaudio.DefaultHostApi()
	if err != nil {
		return nil, nil, fmt.Errorf("default host api: %w", err)
	}

	for _, d := range api.Devices {
		if d.MaxInputChannels > 0 {
			inputs = append(inputs, Device{
				Index:       d.Index,
				Name:        d.Name,
				MaxChannels: d.MaxInputChannels,
			})
		}
		if d.MaxOutputChannels > 0 {
			outputs = append(outputs, Device{
				Index:       d.Index,
				Name:        d.Name,
				MaxChannels: d.MaxOutputChannels,
			})
		}
	}
	return inputs, outputs, nil
}

func deviceByIndex(index int) (*portaudio.DeviceInfo, error) {
	api, err := portaudio.DefaultHostApi()
	if err != nil {
		return nil, err
	}
	for _, d := range api.Devices {
		if d.Index == index {
			return d, nil
		}
	}
	return nil, fmt.Errorf("device index %d not found", index)
}

func New(inputIdx, outputIdx, sampleRate, framesPerBuffer, outputChannel int) (*Engine, error) {
	inDev, err := deviceByIndex(inputIdx)
	if err != nil {
		return nil, fmt.Errorf("input device: %w", err)
	}
	outDev, err := deviceByIndex(outputIdx)
	if err != nil {
		return nil, fmt.Errorf("output device: %w", err)
	}

	channels := 1
	if inDev.MaxInputChannels > 1 && outDev.MaxOutputChannels > 1 {
		channels = 2
	}

	if outputChannel < 1 {
		outputChannel = 1
	}
	if outputChannel > outDev.MaxOutputChannels {
		outputChannel = outDev.MaxOutputChannels
	}

	e := &Engine{
		inputDevice:    inDev,
		outputDevice:   outDev,
		sampleRate:     sampleRate,
		framesBuf:      framesPerBuffer,
		channels:       channels,
		outputChannel:  outputChannel,
		outputChannels: outDev.MaxOutputChannels,
		gain:           1.0,
		eq:             NewEQ(float64(sampleRate)),
	}

	outChans := outDev.MaxOutputChannels
	if outChans < 1 {
		outChans = 1
	}

	p := portaudio.StreamParameters{
		Input: portaudio.StreamDeviceParameters{
			Device:   inDev,
			Channels: channels,
			Latency:  inDev.DefaultLowInputLatency,
		},
		Output: portaudio.StreamDeviceParameters{
			Device:   outDev,
			Channels: outChans,
			Latency:  outDev.DefaultLowOutputLatency,
		},
		SampleRate:      float64(sampleRate),
		FramesPerBuffer: framesPerBuffer,
		Flags:           portaudio.NoFlag,
	}

	stream, err := portaudio.OpenStream(p, e.processCallback)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	e.stream = stream

	return e, nil
}

func (e *Engine) processCallback(input []float32, output []float32) {
	var inSum float64
	for _, s := range input {
		inSum += float64(s) * float64(s)
	}
	if len(input) > 0 {
		inSum = math.Sqrt(inSum / float64(len(input)))
	}

	e.gainMu.RLock()
	gain := e.gain
	muted := atomic.LoadInt32(&e.muted) == 1
	e.gainMu.RUnlock()

	// Zero all output channels first
	for i := range output {
		output[i] = 0
	}

	// Calculate samples per frame for output (interleaved multi-channel)
	outChans := e.outputChannels
	if outChans < 1 {
		outChans = 1
	}
	nFrames := len(output) / outChans
	inFrames := len(input) / e.channels
	if inFrames < nFrames {
		nFrames = inFrames
	}

	var outSum float64
	// Route input to the selected output channel (1-indexed)
	outCh := e.outputChannel - 1 // 0-indexed
	for f := 0; f < nFrames; f++ {
		// Sum input channels for this frame into one sample
		var sample float32
		for ch := 0; ch < e.channels; ch++ {
			sample += input[f*e.channels+ch]
		}
		if e.channels > 1 {
			sample /= float32(e.channels) // average
		}
		sample = float32(e.eq.ProcessSample(float64(sample)))
		if muted {
			sample = 0
		} else {
			sample *= gain
		}
		output[f*outChans+outCh] = sample
		outSum += float64(sample) * float64(sample)
	}

	if nFrames > 0 {
		outSum = math.Sqrt(outSum / float64(nFrames))
	}

	e.statsMu.Lock()
	e.stats.InputLevel = inSum
	e.stats.OutputLevel = outSum
	e.statsMu.Unlock()
}

func (e *Engine) Start() error {
	return e.stream.Start()
}

func (e *Engine) Stop() error {
	return e.stream.Stop()
}

func (e *Engine) Close() error {
	return e.stream.Close()
}

func (e *Engine) ChangeBuffer(newFrames int) error {
	if newFrames < 16 {
		newFrames = 16
	}
	if newFrames > 4096 {
		newFrames = 4096
	}

	e.stream.Stop()
	e.stream.Close()

	outChans := e.outputChannels
	if outChans < 1 {
		outChans = 1
	}

	p := portaudio.StreamParameters{
		Input: portaudio.StreamDeviceParameters{
			Device:   e.inputDevice,
			Channels: e.channels,
			Latency:  e.inputDevice.DefaultLowInputLatency,
		},
		Output: portaudio.StreamDeviceParameters{
			Device:   e.outputDevice,
			Channels: outChans,
			Latency:  e.outputDevice.DefaultLowOutputLatency,
		},
		SampleRate:      float64(e.sampleRate),
		FramesPerBuffer: newFrames,
		Flags:           portaudio.NoFlag,
	}

	stream, err := portaudio.OpenStream(p, e.processCallback)
	if err != nil {
		return fmt.Errorf("reopen stream: %w", err)
	}

	e.stream = stream
	e.framesBuf = newFrames

	return e.stream.Start()
}

func (e *Engine) LatencyMS() float64 {
	deviceInMS := e.inputDevice.DefaultLowInputLatency.Seconds() * 1000
	deviceOutMS := e.outputDevice.DefaultLowOutputLatency.Seconds() * 1000
	bufMS := float64(e.framesBuf) / float64(e.sampleRate) * 1000
	return deviceInMS + deviceOutMS + bufMS
}

func (e *Engine) InputLatencyMS() float64 {
	deviceMS := e.inputDevice.DefaultLowInputLatency.Seconds() * 1000
	bufMS := float64(e.framesBuf) / float64(e.sampleRate) * 1000
	return deviceMS + bufMS
}

func (e *Engine) OutputLatencyMS() float64 {
	deviceMS := e.outputDevice.DefaultLowOutputLatency.Seconds() * 1000
	bufMS := float64(e.framesBuf) / float64(e.sampleRate) * 1000
	return deviceMS + bufMS
}

func (e *Engine) SetGain(db float64) {
	linear := float32(math.Pow(10, db/20.0))
	e.gainMu.Lock()
	e.gain = linear
	e.gainMu.Unlock()
}

func (e *Engine) GetGain() float64 {
	e.gainMu.RLock()
	g := e.gain
	e.gainMu.RUnlock()
	if g <= 0 {
		return -100.0
	}
	return 20.0 * math.Log10(float64(g))
}

func (e *Engine) ToggleMute() bool {
	for {
		old := atomic.LoadInt32(&e.muted)
		var new int32
		if old == 0 {
			new = 1
		}
		if atomic.CompareAndSwapInt32(&e.muted, old, new) {
			return new == 1
		}
	}
}

func (e *Engine) IsMuted() bool {
	return atomic.LoadInt32(&e.muted) == 1
}

func (e *Engine) Stats() Stats {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return e.stats
}

func (e *Engine) InputDeviceName() string {
	return e.inputDevice.Name
}

func (e *Engine) OutputDeviceName() string {
	return e.outputDevice.Name
}

func (e *Engine) SetOutputChannel(ch int) {
	if ch < 1 {
		ch = 1
	}
	if ch > e.outputChannels {
		ch = e.outputChannels
	}
	e.outputChannel = ch
}

func (e *Engine) OutputChannel() int {
	return e.outputChannel
}

func (e *Engine) OutputChannels() int {
	return e.outputChannels
}

func (e *Engine) SampleRate() int {
	return e.sampleRate
}

func (e *Engine) FramesPerBuffer() int {
	return e.framesBuf
}

func (e *Engine) Channels() int {
	return e.channels
}

func (e *Engine) InputLatency() time.Duration {
	return e.inputDevice.DefaultLowInputLatency
}

func (e *Engine) OutputLatency() time.Duration {
	return e.outputDevice.DefaultLowOutputLatency
}

func RMSdB(level float64) float64 {
	if level <= 0 {
		return -120.0
	}
	return 20.0 * math.Log10(level)
}

func (e *Engine) EQBands() int {
	return eqBands
}

func (e *Engine) EQFrequencies() []float64 {
	return e.eq.Frequencies()
}

func (e *Engine) SetEQBand(band, gainDB int) {
	e.eq.SetBand(band, float64(gainDB))
}

func (e *Engine) EQBandGain(band int) int {
	return int(math.Round(e.eq.BandGain(band)))
}

func (e *Engine) ResetEQ() {
	e.eq.Reset()
}

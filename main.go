package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/rvaldez/gotone/engine"
	"github.com/rvaldez/gotone/tui"
)

var version = "0.1.0"

func main() {
	listDevices := flag.Bool("list-devices", false, "List available audio devices and exit")
	inputIdx := flag.Int("input", -1, "Input device index")
	outputIdx := flag.Int("output", -1, "Output device index")
	outputChannel := flag.Int("channel", 1, "Output channel (1-indexed)")
	sampleRate := flag.Int("sample-rate", 48000, "Sample rate in Hz")
	framesBuf := flag.Int("buffer-size", 256, "Frames per buffer (lower = less latency, more CPU)")
	gainDB := flag.Float64("gain", 0.0, "Initial gain in dB")
	flag.Parse()

	if err := engine.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing audio: %v\n", err)
		os.Exit(1)
	}
	defer engine.Terminate()

	if *listDevices {
		listAudioDevices()
		return
	}

	inputs, outputs, err := engine.Devices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing devices: %v\n", err)
		os.Exit(1)
	}

	if len(inputs) == 0 {
		fmt.Fprintln(os.Stderr, "No input devices found")
		os.Exit(1)
	}
	if len(outputs) == 0 {
		fmt.Fprintln(os.Stderr, "No output devices found")
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)

	// Interactive device selection
	if *inputIdx == -1 {
		*inputIdx = selectDevice(reader, "INPUT", inputs)
	}
	if *outputIdx == -1 {
		*outputIdx = selectDevice(reader, "OUTPUT", outputs)
	}
	if *outputChannel == 1 {
		// Show channel selection if device has multiple outputs
		outMax := 0
		for _, d := range outputs {
			if d.Index == *outputIdx {
				outMax = d.MaxChannels
				break
			}
		}
		if outMax > 1 {
			*outputChannel = selectChannel(reader, outMax)
		}
	}

	fmt.Println()

	eng, err := engine.New(*inputIdx, *outputIdx, *sampleRate, *framesBuf, *outputChannel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating audio engine: %v\n", err)
		os.Exit(1)
	}

	eng.SetGain(*gainDB)

	tui.SetVersion(version)

	if err := eng.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting audio: %v\n", err)
		os.Exit(1)
	}

	// Run TUI
	ui := tui.New(eng)
	ui.Start()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGWINCH:
				ui.NotifyResize()
			default:
				ui.Stop()
				eng.Stop()
				os.Exit(0)
			}
		}
	}()

	// Block until TUI signals quit
	<-ui.Done()
	eng.Stop()
}

func selectDevice(reader *bufio.Reader, kind string, devices []engine.Device) int {
	for {
		fmt.Printf("\033[1mSelect %s device:\033[0m\n", kind)
		for _, d := range devices {
			fmt.Printf("  [%2d] %s\n", d.Index, d.Name)
		}
		fmt.Print("> ")

		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)

		if line == "" {
			// Default to first device
			fmt.Printf("  → %s\n", devices[0].Name)
			return devices[0].Index
		}

		idx, err := strconv.Atoi(line)
		if err != nil {
			fmt.Printf("  Invalid input, enter a number\n\n")
			continue
		}

		for _, d := range devices {
			if d.Index == idx {
				return idx
			}
		}
		fmt.Printf("  Device %d not found, try again\n\n", idx)
	}
}

func selectChannel(reader *bufio.Reader, maxChannels int) int {
	for {
		fmt.Printf("\033[1mSelect output channel (1-%d):\033[0m\n", maxChannels)
		fmt.Print("> ")

		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)

		if line == "" {
			fmt.Printf("  → Channel 1\n")
			return 1
		}

		ch, err := strconv.Atoi(line)
		if err != nil || ch < 1 || ch > maxChannels {
			fmt.Printf("  Enter a number between 1 and %d\n\n", maxChannels)
			continue
		}
		return ch
	}
}

func listAudioDevices() {
	inputs, outputs, err := engine.Devices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== INPUT DEVICES ===")
	for _, d := range inputs {
		fmt.Printf("  [%2d] %s (max %d ch)\n", d.Index, d.Name, d.MaxChannels)
	}

	fmt.Println("\n=== OUTPUT DEVICES ===")
	for _, d := range outputs {
		fmt.Printf("  [%2d] %s (max %d ch)\n", d.Index, d.Name, d.MaxChannels)
	}
}

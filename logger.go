package test161

import (
	"regexp"
	"time"

	"github.com/jay1999ke/test161/expect"
)

// Recv processes new sys161 output and restarts the progress timer
func (t *Test) Recv(receivedTime time.Time, received []byte) {

	// This is a slightly hacky way to ensure that getStats isn't started until
	// sys161 has began to run. (Starting it too early causes the unix socket
	// connect to fail.) statStarted is only used once and doesn't need to be
	// protected.
	if !t.statStarted {
		go t.getStats()
		t.statStarted = true
	}

	// Parse some new incoming data. Frequently just a single byte but sometimes
	// more.
	t.L.Lock()
	defer t.L.Unlock()

	// Mark progress for the progress timeout.
	t.progressTime = float64(t.SimTime)

	for _, b := range received {
		// Add timestamps to the beginning of each line.
		if t.currentOutput.WallTime == 0.0 {
			t.currentOutput.WallTime = t.getWallTime()
			t.currentOutput.SimTime = t.SimTime
		}
		t.currentOutput.Buffer.WriteByte(b)
		if b == '\n' {
			t.currentOutput.Line = t.currentOutput.Buffer.String()
			t.outputLineComplete()
			t.currentCommand.Output = append(t.currentCommand.Output, t.currentOutput)
			t.env.notifyAndLogErr("Update Command Output", t.currentCommand, MSG_PERSIST_UPDATE, MSG_FIELD_OUTPUT)
			t.currentOutput = &OutputLine{}
		}
	}
}

// Unused parts of the expect.Logger interface
func (t *Test) Send(time.Time, []byte)                      {}
func (t *Test) SendMasked(time.Time, []byte)                {}
func (t *Test) RecvNet(time.Time, []byte)                   {}
func (t *Test) RecvEOF(time.Time)                           {}
func (t *Test) ExpectCall(time.Time, *regexp.Regexp)        {}
func (t *Test) ExpectReturn(time.Time, expect.Match, error) {}
func (t *Test) Close(time.Time)                             {}

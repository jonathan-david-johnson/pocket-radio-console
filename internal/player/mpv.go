package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// lookPath is indirected so the missing-mpv path is unit-testable with a fake.
var lookPath = exec.LookPath

// mpvPlayer drives an mpv subprocess over its JSON IPC unix socket.
type mpvPlayer struct {
	cmd    *exec.Cmd
	conn   net.Conn
	socket string

	mu          sync.RWMutex
	state       PlaybackState
	pendingSeek time.Duration // seek issued after file-loaded when > 0

	events  chan PlayerEvent
	reqID   int
	closeCh chan struct{}
	once    sync.Once
}

// New starts an idle mpv subprocess and returns a Player bound to it. Returns
// ErrMpvNotFound if mpv is not on PATH.
func New() (Player, error) {
	if _, err := lookPath("mpv"); err != nil {
		return nil, ErrMpvNotFound
	}

	socket := filepath.Join(os.TempDir(), fmt.Sprintf("pocket-radio-mpv-%d.sock", os.Getpid()))
	_ = os.Remove(socket)

	cmd := exec.Command("mpv",
		"--idle=yes",
		"--no-video",
		"--no-terminal",
		"--input-ipc-server="+socket,
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}

	conn, err := dialSocket(socket, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("dial mpv ipc: %w", err)
	}

	p := &mpvPlayer{
		cmd:     cmd,
		conn:    conn,
		socket:  socket,
		events:  make(chan PlayerEvent, 64),
		closeCh: make(chan struct{}),
	}

	// Observe the properties the UI needs.
	_ = p.command("observe_property", 1, "time-pos")
	_ = p.command("observe_property", 2, "duration")
	_ = p.command("observe_property", 3, "pause")
	_ = p.command("observe_property", 4, "metadata")
	// file-loaded fires when mpv has opened the file and is ready to seek.
	_ = p.command("observe_property", 5, "playback-restart")

	go p.readLoop()
	return p, nil
}

// dialSocket retries until mpv has created the IPC socket or timeout elapses.
func dialSocket(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *mpvPlayer) command(args ...interface{}) error {
	p.mu.Lock()
	p.reqID++
	id := p.reqID
	p.mu.Unlock()
	payload := map[string]interface{}{"command": args, "request_id": id}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = p.conn.Write(b)
	return err
}

func (p *mpvPlayer) setProperty(name string, value interface{}) error {
	return p.command("set_property", name, value)
}

// Load replaces the current file. startAt > 0 seeks after file-loaded fires,
// which is more reliable for streaming URLs than loadfile's start= option.
func (p *mpvPlayer) Load(url string, startAt time.Duration) error {
	p.mu.Lock()
	p.state = PlaybackState{Playing: true}
	p.pendingSeek = startAt
	p.mu.Unlock()
	if err := p.command("loadfile", url, "replace"); err != nil {
		return err
	}
	// loadfile inherits mpv's current pause property, so switching to a new
	// source while paused would load it silently. Load means "play now" — force
	// pause off so a single keypress switches and plays.
	return p.setProperty("pause", false)
}

func (p *mpvPlayer) Pause() error  { return p.setProperty("pause", true) }
func (p *mpvPlayer) Resume() error { return p.setProperty("pause", false) }

func (p *mpvPlayer) Seek(to time.Duration) error {
	return p.command("seek", to.Seconds(), "absolute")
}

func (p *mpvPlayer) State() PlaybackState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *mpvPlayer) Events() <-chan PlayerEvent { return p.events }

func (p *mpvPlayer) Close() error {
	p.once.Do(func() {
		close(p.closeCh)
		_ = p.command("quit")
		if p.conn != nil {
			_ = p.conn.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
		_ = os.Remove(p.socket)
	})
	return nil
}

// ipcMessage is an mpv IPC line (event or command reply).
type ipcMessage struct {
	Event     string          `json:"event"`
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	Reason    string          `json:"reason"`
	RequestID int             `json:"request_id"`
}

func (p *mpvPlayer) readLoop() {
	scanner := bufio.NewScanner(p.conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-p.closeCh:
			return
		default:
		}
		var msg ipcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		p.handle(msg)
	}
}

func (p *mpvPlayer) handle(msg ipcMessage) {
	switch msg.Event {
	case "property-change":
		p.handleProperty(msg)
	case "file-loaded":
		// File is open and ready; execute any pending resume seek.
		p.mu.Lock()
		seek := p.pendingSeek
		p.pendingSeek = 0
		p.mu.Unlock()
		if seek > 0 {
			_ = p.command("seek", seek.Seconds(), "absolute")
		}
	case "end-file":
		// "eof" means a finite file finished; other reasons (stop, quit) don't.
		if msg.Reason == "eof" {
			p.emit(PlayerEvent{Kind: Ended})
		}
	}
}

func (p *mpvPlayer) handleProperty(msg ipcMessage) {
	switch msg.Name {
	case "time-pos":
		var sec float64
		if json.Unmarshal(msg.Data, &sec) == nil {
			p.mu.Lock()
			p.state.Position = time.Duration(sec * float64(time.Second))
			p.mu.Unlock()
			p.emit(PlayerEvent{Kind: Tick})
		}
	case "duration":
		var sec float64
		if json.Unmarshal(msg.Data, &sec) == nil {
			p.mu.Lock()
			p.state.Duration = time.Duration(sec * float64(time.Second))
			p.state.IsLive = sec == 0
			p.mu.Unlock()
		}
	case "pause":
		var paused bool
		if json.Unmarshal(msg.Data, &paused) == nil {
			p.mu.Lock()
			p.state.Playing = !paused
			p.mu.Unlock()
			p.emit(PlayerEvent{Kind: Tick})
		}
	case "metadata":
		var meta map[string]string
		if json.Unmarshal(msg.Data, &meta) == nil && len(meta) > 0 {
			p.emit(PlayerEvent{Kind: Metadata, Metadata: meta})
		}
	}
}

func (p *mpvPlayer) emit(ev PlayerEvent) {
	select {
	case p.events <- ev:
	case <-p.closeCh:
	default: // drop if the UI is not draining fast enough
	}
}

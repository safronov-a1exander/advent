package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Stdio — транспорт к локальному серверу: клиент запускает его
// подпроцессом и говорит через stdin/stdout, по одному JSON-сообщению
// на строку. stderr сервера — его журнал, в протокол он не попадает.
//
// Ответы приходят в том порядке, в каком сервер их закончил, а не
// в каком ушли вопросы, поэтому читает их одна горутина и раздаёт
// ждущим по id.
type Stdio struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu      sync.Mutex
	writeMu sync.Mutex
	waiting map[int64]chan message
	trace   Trace
	closed  bool
	err     error // почему поток ответов кончился
	done    chan struct{}
	stderr  *tailBuffer
}

// NewStdio запускает сервер командой name с аргументами args.
func NewStdio(name string, args []string, env []string) (*Stdio, error) {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 4096}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("не запустился %s: %w", name, err)
	}
	s := &Stdio{cmd: cmd, stdin: in, waiting: map[int64]chan message{}, done: make(chan struct{}), stderr: tail}
	go s.read(out)
	return s, nil
}

// NewStdioPipe — тот же транспорт поверх готовых потоков, без подпроцесса.
// Для тестов и для сервера, запущенного в том же процессе.
func NewStdioPipe(w io.WriteCloser, r io.Reader) *Stdio {
	s := &Stdio{stdin: w, waiting: map[int64]chan message{}, done: make(chan struct{}), stderr: &tailBuffer{max: 4096}}
	go s.read(r)
	return s
}

func (s *Stdio) SetTrace(t Trace) {
	s.mu.Lock()
	s.trace = t
	s.mu.Unlock()
}

func (s *Stdio) emit(dir Direction, raw []byte) {
	s.mu.Lock()
	t := s.trace
	s.mu.Unlock()
	if t != nil {
		t(dir, raw)
	}
}

func (s *Stdio) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		raw := append([]byte(nil), line...)
		s.emit(In, raw)
		var m message
		if json.Unmarshal(raw, &m) != nil {
			continue // мусор в stdout — сервер виноват, но вызов ломать не будем
		}
		if !m.isResponse() {
			if m.Method == "ping" && len(m.ID) > 0 {
				// Встречный ping: сервер проверяет, жив ли клиент.
				s.write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, m.ID)))
			}
			continue
		}
		id, ok := idOf(m.ID)
		if !ok {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[id]
		delete(s.waiting, id)
		s.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	s.mu.Lock()
	s.err = sc.Err()
	if s.err == nil {
		s.err = errors.New("сервер закрыл stdout")
	}
	if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
		s.err = fmt.Errorf("%w; stderr: %s", s.err, lastLines(tail, 3))
	}
	s.mu.Unlock()
	close(s.done)
}

func (s *Stdio) write(body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.emit(Out, body)
	_, err := s.stdin.Write(append(body, '\n'))
	return err
}

func (s *Stdio) Call(ctx context.Context, id int64, body []byte) (message, error) {
	ch := make(chan message, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return message{}, errors.New("соединение закрыто")
	}
	s.waiting[id] = ch
	s.mu.Unlock()

	if err := s.write(body); err != nil {
		s.forget(id)
		return message{}, fmt.Errorf("запись в stdin сервера: %w", err)
	}
	select {
	case m := <-ch:
		return m, nil
	case <-s.done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		return message{}, fmt.Errorf("сервер завершился, не ответив: %w", err)
	case <-ctx.Done():
		s.forget(id)
		return message{}, ctx.Err()
	}
}

func (s *Stdio) forget(id int64) {
	s.mu.Lock()
	delete(s.waiting, id)
	s.mu.Unlock()
}

func (s *Stdio) Notify(_ context.Context, body []byte) error { return s.write(body) }

// Close закрывает stdin — по спецификации это и есть сигнал серверу
// завершаться. Если он не ушёл сам за пару секунд, процесс снимается.
func (s *Stdio) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.stdin.Close()
	if s.cmd == nil {
		return nil
	}
	exited := make(chan struct{})
	go func() { s.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		s.cmd.Process.Kill()
		<-exited
	}
	return nil
}

// tailBuffer — последние байты stderr сервера: пригодятся, если он упал.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

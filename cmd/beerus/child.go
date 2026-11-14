package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	restartMin    = time.Second
	restartMax    = 30 * time.Second
	healthyAfter  = time.Minute
	fastFailLimit = 5
	logLineMax    = 4096
	logCutMark    = "..."
	logTailLines  = 10
)

type child struct {
	id        string
	print     string
	quit      chan struct{}
	quitOnce  sync.Once
	done      chan struct{}
	firstExit chan struct{}
	exit      error
	tail      []string
}

func (c *child) stop() {
	c.quitOnce.Do(func() { close(c.quit) })
}

func (c *child) stopped() bool {
	select {
	case <-c.quit:
		return true
	default:
		return false
	}
}

func (c *child) remember(line string) {
	c.tail = append(c.tail, line)
	if len(c.tail) > logTailLines {
		c.tail = c.tail[len(c.tail)-logTailLines:]
	}
}

func (s *supervisor) launch(id, print string, args []string) *child {
	c := &child{id: id, print: print, quit: make(chan struct{}), done: make(chan struct{}), firstExit: make(chan struct{})}
	go s.keep(c, args)
	return c
}

func (s *supervisor) keep(c *child, args []string) {
	defer close(c.done)
	delay := restartMin
	fast := 0
	for attempt := 0; ; attempt++ {
		began := s.clock.Real()
		c.tail = nil
		err := s.runOnce(c, args)
		if c.stopped() {
			return
		}
		if attempt == 0 {
			c.exit = err
			close(c.firstExit)
		}
		if s.clock.Real().Sub(began) >= healthyAfter {
			delay, fast = restartMin, 0
		} else {
			fast++
		}
		if fast >= fastFailLimit {
			s.log.Error("participant keeps failing; restarting", "participant", c.id, "exit", exitText(err), "failures", fast, "stderr", strings.Join(c.tail, "\n"))
		} else {
			s.log.Warn("participant exited; restarting", "participant", c.id, "exit", exitText(err))
		}
		timer := time.NewTimer(delay)
		select {
		case <-c.quit:
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, restartMax)
	}
}

func (s *supervisor) runOnce(c *child, args []string) error {
	cmd := exec.Command(s.exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdin of %s: %w", c.id, err)
	}
	logR, logW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("open log pipe of %s: %w", c.id, err)
	}
	cmd.Stderr = logW
	if err := cmd.Start(); err != nil {
		logR.Close()
		logW.Close()
		return fmt.Errorf("start %s: %w", c.id, err)
	}
	logW.Close()
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		defer logR.Close()
		s.copyLogs(c, logR)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		<-logged
		return err
	case <-c.quit:
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = stdin.Close()
	grace := time.NewTimer(stopGrace)
	defer grace.Stop()
	select {
	case err := <-exited:
		<-logged
		return err
	case <-grace.C:
	}
	_ = cmd.Process.Kill()
	err = <-exited
	<-logged
	return err
}

func (s *supervisor) copyLogs(c *child, r io.Reader) {
	br := bufio.NewReaderSize(r, logLineMax)
	for {
		line, more, err := br.ReadLine()
		if len(line) > 0 {
			text := string(line)
			if more {
				text += logCutMark
			}
			c.remember(text)
			_, _ = fmt.Fprintf(s.logs, "%s | %s\n", c.id, text)
		}
		for more && err == nil {
			_, more, err = br.ReadLine()
		}
		if err != nil {
			return
		}
	}
}

func exitText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	restartMin   = time.Second
	restartMax   = 30 * time.Second
	healthyAfter = time.Minute
	logLineMax   = 4096
	logCutMark   = "..."
)

type child struct {
	id        string
	stop      context.CancelFunc
	done      chan struct{}
	firstExit chan struct{}
	exit      error
}

func (s *supervisor) launch(ctx context.Context, id string, args []string) *child {
	ctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	c := &child{id: id, stop: stop, done: make(chan struct{}), firstExit: make(chan struct{})}
	go s.keep(ctx, c, args)
	return c
}

func (s *supervisor) keep(ctx context.Context, c *child, args []string) {
	defer close(c.done)
	delay := restartMin
	for attempt := 0; ; attempt++ {
		began := s.clock.Real()
		err := s.runOnce(ctx, c.id, args)
		if ctx.Err() != nil {
			return
		}
		if attempt == 0 {
			c.exit = err
			close(c.firstExit)
		}
		if s.clock.Real().Sub(began) >= healthyAfter {
			delay = restartMin
		}
		s.log.Warn("participant exited; restarting", "participant", c.id, "exit", exitText(err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, restartMax)
	}
}

func (s *supervisor) runOnce(ctx context.Context, id string, args []string) error {
	cmd := exec.Command(s.exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdin of %s: %w", id, err)
	}
	logR, logW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("open log pipe of %s: %w", id, err)
	}
	cmd.Stderr = logW
	if err := cmd.Start(); err != nil {
		logR.Close()
		logW.Close()
		return fmt.Errorf("start %s: %w", id, err)
	}
	logW.Close()
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		defer logR.Close()
		s.copyLogs(id, logR)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		<-logged
		return err
	case <-ctx.Done():
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

func (s *supervisor) copyLogs(id string, r io.Reader) {
	br := bufio.NewReaderSize(r, logLineMax)
	for {
		line, more, err := br.ReadLine()
		if len(line) > 0 {
			cut := ""
			if more {
				cut = logCutMark
			}
			_, _ = fmt.Fprintf(s.logs, "%s | %s%s\n", id, line, cut)
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

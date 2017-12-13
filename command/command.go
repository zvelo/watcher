package command

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const DefaultTimeout = 5 * time.Second

type Command interface {
	String() string
	Path() string
	LogToFile(name string) error
	SetTimeout(timeout time.Duration)
	Start() error
	Restart() error
	Signal(sig os.Signal)
	Done() <-chan struct{}
	Err() error
}

type command struct {
	*exec.Cmd

	timeout     time.Duration
	logFile     string
	args        []string
	file        *os.File
	returned    chan struct{}
	done        chan struct{}
	restartFlag uint32
	path        string

	exitErrMu sync.RWMutex
	exitErr   error

	once sync.Once
}

func (c *command) Done() <-chan struct{} {
	return c.done
}

func (c *command) Err() error {
	c.exitErrMu.RLock()
	defer c.exitErrMu.RUnlock()
	return c.exitErr
}

func ExitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}

	eerr, ok := err.(*exec.ExitError)
	if !ok {
		return 0, false
	}

	if ws, ok := eerr.Sys().(syscall.WaitStatus); ok {
		return ws.ExitStatus(), true
	}

	return 0, false
}

func New(cmd string, args ...string) (Command, error) {
	if len(args) == 0 {
		return nil, errors.New("invalid args")
	}

	c := command{
		args:    args,
		done:    make(chan struct{}),
		timeout: DefaultTimeout,
	}

	var err error

	if c.path, err = exec.LookPath(cmd); err != nil {
		return nil, err
	}

	if c.path, err = filepath.EvalSymlinks(c.path); err != nil {
		return nil, err
	}

	return &c, nil
}

func (c *command) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
}

func (c *command) LogToFile(name string) error {
	c.closeLog()

	if name == "" {
		return nil
	}

	var err error
	if c.file, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err != nil {
		return err
	}

	c.logFile = name

	return nil
}

func (c *command) Start() error {
	c.Cmd = &exec.Cmd{
		Path:   c.path,
		Args:   c.args,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{
			Setpgid: true, // prevent external signals from getting to the subprocess
		},
	}

	if c.file != nil {
		c.Stdout = c.file
		c.Stderr = c.file
	}

	c.returned = make(chan struct{})

	log.Printf("command: starting %s", c.path)

	if err := c.Cmd.Start(); err != nil {
		return err
	}

	go c.once.Do(c.trapSignals)
	go c.wait()

	log.Printf("command: started")

	return nil
}

func (c *command) closeLog() {
	if c.file == nil {
		return
	}

	if err := c.file.Close(); err != nil {
		log.Printf("command: error closing logfile %s: %s", c.logFile, err)
	}
}

func (c *command) restarting() bool {
	return atomic.LoadUint32(&c.restartFlag) != 0
}

func (c *command) trapSignals() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs)

	for {
		select {
		case sig := <-sigs:
			if s, ok := sig.(syscall.Signal); ok {
				switch s {
				case syscall.SIGCHLD:
					continue
				}
			}
			c.Signal(sig)
		case <-c.Done():
			return
		}
	}
}

func (c *command) wait() {
	err := c.Wait()

	if code, ok := ExitCode(err); ok && code != 0 {
		log.Printf("command: returned, exit code: %d, error: %s", code, err)
	} else if err != nil {
		log.Println("command: returned error:", err)
	} else {
		log.Println("command: returned successfully")
	}

	close(c.returned)

	if c.restarting() {
		return
	}

	c.closeLog()

	c.exitErrMu.Lock()
	defer c.exitErrMu.Unlock()

	c.exitErr = err
	close(c.done)
}

func (c *command) Signal(sig os.Signal) {
	var stopping bool

	if s, ok := sig.(syscall.Signal); ok {
		// override SIGINT with SIGTERM
		if s == syscall.SIGINT {
			sig = syscall.SIGTERM
		}

		switch s {
		case syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT:
			stopping = true
		case syscall.SIGHUP:
			log.Printf("command: received '%s', restarting command", sig)
			if err := c.Restart(); err != nil {
				log.Println("command: error restarting command:", err)
			}
			return
		}
	}

	log.Printf("command: sending '%s' signal", sig)

	if err := c.Process.Signal(sig); err != nil {
		log.Printf("command: error sending '%s' signal: %s", sig, err)
		return
	}

	if !stopping || c.timeout == 0 {
		return
	}

	select {
	case <-c.returned:
	case <-time.After(c.timeout):
		log.Printf("command: %s timeout exceeded, killing", c.timeout)
		if err := c.Process.Kill(); err != nil {
			log.Printf("command: error killing command: %s", err)
			return
		}
	}
}

func (c *command) Restart() error {
	atomic.StoreUint32(&c.restartFlag, 1)
	defer atomic.StoreUint32(&c.restartFlag, 0)

	c.Signal(syscall.SIGTERM)
	<-c.returned

	return c.Start()
}

func (c *command) String() string {
	return c.args[0]
}

func (c *command) Path() string {
	return c.path
}

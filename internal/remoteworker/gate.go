package remoteworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const gateAwaitTimeout = 30 * time.Second

type gateReceipt struct {
	Status     string `json:"status"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

type gateRunner func(context.Context, io.Reader, io.Writer) error

func RunGate(ctx context.Context, requestPath, gate string, output io.Writer) error {
	return runGate(ctx, requestPath, gate, output, func(ctx context.Context, input io.Reader, result io.Writer) error {
		return Run(ctx, input, result, io.Discard)
	})
}

func runGate(ctx context.Context, requestPath, gate string, output io.Writer, runner gateRunner) error {
	if !safeSegment(gate) || filepath.Base(requestPath) == "." || filepath.Base(requestPath) == string(filepath.Separator) {
		return ErrInvalidRequest
	}
	directoryPath := filepath.Dir(requestPath)
	directory, err := openGateDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	request, err := openGateFile(directory, filepath.Base(requestPath))
	if err != nil {
		return err
	}
	defer request.Close()
	if err := writeGateFile(directory, gate+".intent", []byte("pending\n")); err != nil {
		return err
	}
	var result bytes.Buffer
	runErr := runner(ctx, request, &result)
	if result.Len() > DiagnosticLimit {
		runErr = errors.Join(runErr, errors.New("remote-worker gate result exceeds limit"))
		result.Reset()
	}
	if output != nil {
		_, _ = io.Copy(output, bytes.NewReader(result.Bytes()))
	}
	receipt := gateReceipt{Status: "success"}
	if runErr != nil {
		receipt.Status = "failure"
		receipt.Diagnostic = boundedDiagnostic(runErr)
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return errors.Join(runErr, err)
	}
	body = append(body, '\n')
	if err := writeGateFile(directory, gate+".result.partial", body); err != nil {
		return errors.Join(runErr, err)
	}
	if err := unix.Linkat(int(directory.Fd()), gate+".result.partial", int(directory.Fd()), gate+".result", 0); err != nil {
		return errors.Join(runErr, err)
	}
	if err := unix.Unlinkat(int(directory.Fd()), gate+".result.partial", 0); err != nil {
		return errors.Join(runErr, err)
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
}

func AwaitGate(ctx context.Context, directoryPath, gate string) error {
	if !safeSegment(gate) {
		return ErrInvalidRequest
	}
	directory, err := openGateDirectory(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gateAwaitTimeout)
		defer cancel()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt, err := readGateReceipt(directory, gate+".result")
		if err == nil {
			if receipt.Status == "success" {
				return nil
			}
			if receipt.Status == "failure" {
				return fmt.Errorf("remote-worker gate failed: %s", receipt.Diagnostic)
			}
			return ErrInvalidRequest
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func openGateDirectory(path string) (*os.File, error) {
	if path == "" || filepath.Clean(path) != path {
		return nil, ErrInvalidRequest
	}
	start := "."
	trimmed := path
	if filepath.IsAbs(path) {
		start = "/"
		trimmed = strings.TrimPrefix(path, "/")
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if trimmed == "" || trimmed == "." {
		return os.NewFile(uintptr(fd), path), nil
	}
	for _, segment := range strings.Split(trimmed, string(filepath.Separator)) {
		if !safeSegment(segment) {
			_ = unix.Close(fd)
			return nil, ErrInvalidRequest
		}
		next, openErr := unix.Openat(fd, segment, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openGateFile(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrInvalidRequest
	}
	return file, nil
}

func writeGateFile(directory *os.File, name string, body []byte) error {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readGateReceipt(directory *os.File, name string) (gateReceipt, error) {
	var receipt gateReceipt
	file, err := openGateFile(directory, name)
	if err != nil {
		return receipt, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, DiagnosticLimit+1))
	if err != nil || len(body) > DiagnosticLimit {
		return receipt, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, ErrInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return receipt, ErrInvalidRequest
	}
	return receipt, nil
}

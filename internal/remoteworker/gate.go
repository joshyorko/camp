package remoteworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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

func RunGate(ctx context.Context, requestPath, gate string, awaiters int, output io.Writer) error {
	return runGate(ctx, requestPath, gate, awaiters, output, func(ctx context.Context, input io.Reader, result io.Writer) error {
		return Run(ctx, input, result, io.Discard)
	})
}

func runGate(ctx context.Context, requestPath, gate string, awaiters int, output io.Writer, runner gateRunner) error {
	if !safeSegment(gate) || awaiters < 0 || filepath.Base(requestPath) == "." || filepath.Base(requestPath) == string(filepath.Separator) {
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
	generation, err := randomGateID()
	if err != nil {
		return err
	}
	prefix := gate + "." + generation
	if err := writeGateFile(directory, prefix+".intent", []byte("pending\n")); err != nil {
		return err
	}
	if err := replaceGateFile(directory, gate+".current", []byte(generation+"\n")); err != nil {
		return err
	}
	runCtx, cancel := boundedGateContext(ctx)
	defer cancel()
	if err := awaitGateWaiters(runCtx, directory, prefix, awaiters); err != nil {
		return publishGateResult(directory, prefix, err)
	}
	var result bytes.Buffer
	runErr := runner(runCtx, request, &result)
	if result.Len() > DiagnosticLimit {
		runErr = errors.Join(runErr, errors.New("remote-worker gate result exceeds limit"))
		result.Reset()
	}
	if output != nil {
		_, _ = io.Copy(output, bytes.NewReader(result.Bytes()))
	}
	return publishGateResult(directory, prefix, runErr)
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
	ctx, cancel := boundedGateContext(ctx)
	defer cancel()
	waiter, err := randomGateID()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var joined string
	for {
		generation, currentErr := readGateGeneration(directory, gate+".current")
		if currentErr == nil && generation != joined {
			prefix := gate + "." + generation
			if result, resultErr := openGateFile(directory, prefix+".result"); resultErr == nil {
				_ = result.Close()
				joined = ""
			} else if !errors.Is(resultErr, os.ErrNotExist) {
				return resultErr
			} else {
				if err := writeGateFile(directory, prefix+".wait."+waiter, []byte("waiting\n")); err != nil && !errors.Is(err, os.ErrExist) {
					return err
				}
				if confirmed, err := readGateGeneration(directory, gate+".current"); err == nil && confirmed == generation {
					joined = generation
				}
			}
		} else if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
		if joined != "" {
			receipt, err := readGateReceipt(directory, gate+"."+joined+".result")
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
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func boundedGateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, gateAwaitTimeout)
}

func randomGateID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func readGateGeneration(directory *os.File, name string) (string, error) {
	file, err := openGateFile(directory, name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 34))
	if err != nil {
		return "", err
	}
	generation := strings.TrimSuffix(string(body), "\n")
	if len(generation) != 32 {
		return "", ErrInvalidRequest
	}
	if _, err := hex.DecodeString(generation); err != nil {
		return "", ErrInvalidRequest
	}
	return generation, nil
}

func awaitGateWaiters(ctx context.Context, directory *os.File, prefix string, expected int) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		names, err := directory.Readdirnames(-1)
		if err != nil {
			return err
		}
		count := 0
		waitPrefix := prefix + ".wait."
		for _, name := range names {
			if strings.HasPrefix(name, waitPrefix) {
				count++
			}
		}
		if count >= expected {
			return nil
		}
		if _, err := directory.Seek(0, io.SeekStart); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func replaceGateFile(directory *os.File, name string, body []byte) error {
	temporary := name + ".partial." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := writeGateFile(directory, temporary, body); err != nil {
		return err
	}
	if err := unix.Renameat(int(directory.Fd()), temporary, int(directory.Fd()), name); err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		return err
	}
	return directory.Sync()
}

func publishGateResult(directory *os.File, prefix string, runErr error) error {
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
	if err := writeGateFile(directory, prefix+".result.partial", body); err != nil {
		return errors.Join(runErr, err)
	}
	if err := unix.Linkat(int(directory.Fd()), prefix+".result.partial", int(directory.Fd()), prefix+".result", 0); err != nil {
		return errors.Join(runErr, err)
	}
	if err := unix.Unlinkat(int(directory.Fd()), prefix+".result.partial", 0); err != nil {
		return errors.Join(runErr, err)
	}
	if err := directory.Sync(); err != nil {
		return errors.Join(runErr, err)
	}
	return runErr
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

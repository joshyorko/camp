package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type initActivityContextKey struct{}

func (p *ProductionLifecycle) InitInteractive(ctx context.Context, request InitRequest, mode OutputMode, in io.Reader, out io.Writer) error {
	root := request.Root
	if root == "" {
		var err error
		root, err = filepath.Abs(".")
		if err != nil {
			return err
		}
		request.Root = root
	}
	name, err := promptInitName(in, out, root)
	if err != nil {
		return err
	}
	request.Capsule = name
	ctx = context.WithValue(ctx, initActivityContextKey{}, func(message string) error {
		_, err := fmt.Fprintln(out, message)
		return err
	})
	return p.Init(ctx, request, mode, out)
}

func promptInitName(in io.Reader, out io.Writer, root string) (string, error) {
	defaultName := filepath.Base(filepath.Clean(root))
	if _, err := fmt.Fprintf(out, "Camp name [%s]: ", defaultName); err != nil {
		return "", err
	}
	value, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read camp name: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultName
	}
	return value, nil
}

func reportInitActivity(ctx context.Context, message string) error {
	report, _ := ctx.Value(initActivityContextKey{}).(func(string) error)
	if report == nil {
		return nil
	}
	return report(message)
}

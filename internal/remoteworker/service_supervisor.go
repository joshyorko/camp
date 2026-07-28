package remoteworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"

	supervisoradapter "github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/jsonstrict"
)

type serviceSupervisorRequest struct {
	SchemaVersion uint32               `json:"schemaVersion"`
	Request       Request              `json:"request"`
	Worker        domain.ProcessRecord `json:"worker"`
}

func launchServiceSupervisor(ctx context.Context, request Request) (ServiceReceipt, error) {
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return ServiceReceipt{}, err
	}
	workerStatus, err := processes.InspectPID(ctx, os.Getpid())
	if err != nil || !workerStatus.Running {
		return ServiceReceipt{}, errors.Join(err, ErrServiceEvidence)
	}
	worker := remoteProcessRecord(workerStatus.Executable, workerStatus)
	envelope := serviceSupervisorRequest{
		SchemaVersion: ProtocolSchemaVersion, Request: request, Worker: worker,
	}
	input, err := json.Marshal(envelope)
	if err != nil {
		return ServiceReceipt{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return ServiceReceipt{}, err
	}
	command := exec.CommandContext(ctx, executable, "__remote-service-supervisor")
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return ServiceReceipt{}, fmt.Errorf("remote service supervisor: %w: %s", err, boundedDiagnostic(errors.New(stderr.String())))
	}
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ServiceReceipt{}, err
	}
	var receipt ServiceReceipt
	if result.SchemaVersion != ProtocolSchemaVersion || result.Operation != OperationStartServices ||
		json.Unmarshal(result.Receipt, &receipt) != nil {
		return ServiceReceipt{}, ErrServiceEvidence
	}
	if !reflect.DeepEqual(receipt.Worker, worker) || validateServiceActors(receipt.Worker, receipt.Supervisor) != nil ||
		validateServiceEvidence(receipt.Services) != nil {
		return ServiceReceipt{}, ErrServiceEvidence
	}
	return receipt, nil
}

func RunServiceSupervisor(ctx context.Context, input io.Reader, output io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes || jsonstrict.RejectDuplicateKeys(body) != nil {
		return ErrInvalidRequest
	}
	var envelope serviceSupervisorRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return ErrInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest
	}
	requestBody, err := json.Marshal(envelope.Request)
	if err != nil {
		return err
	}
	request, err := DecodeRequest(bytes.NewReader(requestBody))
	if err != nil || envelope.SchemaVersion != ProtocolSchemaVersion || request.Operation != OperationStartServices {
		return ErrInvalidRequest
	}
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return err
	}
	workerStatus, err := processes.Inspect(ctx, envelope.Worker.Identity)
	if err != nil || !workerStatus.Running ||
		!reflect.DeepEqual(remoteProcessRecord(envelope.Worker.DesiredExecutable, workerStatus), envelope.Worker) {
		return errors.Join(err, ErrServiceEvidence)
	}
	supervisorStatus, err := processes.InspectPID(ctx, os.Getpid())
	if err != nil || !supervisorStatus.Running || supervisorStatus.ParentPID != envelope.Worker.Identity.PID {
		return errors.Join(err, ErrServiceEvidence)
	}
	runtimeState := &productionServicesRuntime{worker: envelope.Worker}
	receipt, err := startServices(ctx, request, envelope.Worker, runtimeState)
	if err != nil {
		return err
	}
	return encodeResult(output, OperationStartServices, receipt)
}

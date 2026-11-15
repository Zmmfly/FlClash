//go:build webpanel && !android

package core

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metacubex/wazero"
	"github.com/metacubex/wazero/api"
)

const (
	webEvaluateMemoryPages = 4096
	webEvaluateMaxBytes    = 32 << 20
	webEvaluateMaxConsole  = 16 << 10
	webEvaluateMaxError    = 4 << 10
	webEvaluateTimeout     = 10 * time.Second
	webEvaluateGuestHash   = "1500dcc84476daf2801825c618003028829fa433b5d4973daeb28c8c3acbbe9c"
)

//go:embed web_api_evaluate_guest/quickjs.wasm
var webEvaluateGuest []byte

var webEvaluateAdmission = make(chan struct{}, 2)

type webEvaluateEngine struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
}

type webEvaluateOutput struct {
	JSON    []byte
	Console string
}

type webEvaluateFault struct {
	Code    string
	Message string
}

func (e *webEvaluateFault) Error() string { return e.Message }

func webEvaluateFailure(code, message string) error {
	return &webEvaluateFault{Code: code, Message: message}
}

func newWebEvaluateEngine(ctx context.Context) (*webEvaluateEngine, error) {
	hash := sha256.Sum256(webEvaluateGuest)
	if hex.EncodeToString(hash[:]) != webEvaluateGuestHash {
		return nil, errors.New("evaluator artifact integrity failure")
	}
	cfg := wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(webEvaluateMemoryPages).
		WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, cfg)
	compiled, err := runtime.CompileModule(ctx, webEvaluateGuest)
	if err != nil {
		runtime.Close(ctx)
		return nil, err
	}
	if len(compiled.ImportedFunctions()) != 0 || len(compiled.ImportedMemories()) != 0 {
		runtime.Close(ctx)
		return nil, errors.New("evaluator must not import host capabilities")
	}
	memories := compiled.ExportedMemories()
	memory, ok := memories["memory"]
	if !ok || len(memories) != 1 {
		runtime.Close(ctx)
		return nil, errors.New("evaluator memory is unavailable")
	}
	if max, bounded := memory.Max(); !bounded || max != webEvaluateMemoryPages {
		runtime.Close(ctx)
		return nil, errors.New("evaluator memory has no exact bound")
	}
	return &webEvaluateEngine{runtime: runtime, compiled: compiled}, nil
}

func (e *webEvaluateEngine) Close() error {
	return e.runtime.Close(context.Background())
}

func webEvaluateAcquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, webEvaluateFailure("evaluation_cancelled", "Evaluation was cancelled")
	}
	select {
	case webEvaluateAdmission <- struct{}{}:
		return func() { <-webEvaluateAdmission }, nil
	default:
		return nil, webEvaluateFailure("evaluation_busy", "The isolated evaluator is at capacity")
	}
}

func (e *webEvaluateEngine) Evaluate(ctx context.Context, script, config []byte, name string) (webEvaluateOutput, error) {
	release, err := webEvaluateAcquire(ctx)
	if err != nil {
		return webEvaluateOutput{}, err
	}
	defer release()
	return e.evaluateReserved(ctx, script, config, name)
}

func (e *webEvaluateEngine) evaluateReserved(ctx context.Context, script, config []byte, name string) (result webEvaluateOutput, err error) {
	if len(script) > webEvaluateMaxBytes || len(config) > webEvaluateMaxBytes || len(name) > webEvaluateMaxError {
		return result, webEvaluateFailure("evaluation_input_limit", "Evaluation input exceeds its byte budget")
	}
	if !utf8.Valid(script) || !utf8.Valid(config) || !utf8.ValidString(name) {
		return result, webEvaluateFailure("evaluation_invalid_input", "Evaluation input must be UTF-8")
	}
	ctx, cancel := context.WithTimeout(ctx, webEvaluateTimeout)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result = webEvaluateOutput{}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = webEvaluateFailure("evaluation_timeout", "Evaluation exceeded its time budget")
			} else {
				err = webEvaluateFailure("evaluation_cancelled", "Evaluation was cancelled")
			}
		}
	}()
	module, err := e.runtime.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return result, webEvaluateFailure("evaluation_failed", "The isolated evaluator could not start")
	}
	defer module.Close(context.Background())
	args := make([]uint64, 0, 6)
	for _, value := range [][]byte{script, config, []byte(name)} {
		allocation, err := module.ExportedFunction("allocate").Call(ctx, uint64(len(value)))
		if err != nil || len(allocation) != 1 || allocation[0] == 0 || allocation[0] > uint64(^uint32(0)) {
			return result, webEvaluateFailure("evaluation_memory_limit", "Evaluation exceeded its memory budget")
		}
		if !module.Memory().Write(uint32(allocation[0]), value) {
			return result, webEvaluateFailure("evaluation_failed", "Evaluation input could not be copied")
		}
		args = append(args, allocation[0], uint64(len(value)))
	}
	status, err := module.ExportedFunction("evaluate").Call(ctx, args...)
	if err != nil || len(status) != 1 {
		return result, webEvaluateFailure("evaluation_failed", "Evaluation trapped or exhausted its stack or memory budget")
	}
	limit := webEvaluateMaxBytes
	if status[0] != 0 {
		limit = webEvaluateMaxError
	}
	output, err := webEvaluateRead(ctx, module, "output_pointer", "output_length", limit)
	if err != nil {
		return result, err
	}
	if status[0] != 0 {
		code := "evaluation_script_error"
		if status[0] == 2 {
			code = "evaluation_output_limit"
		}
		message := strings.ToValidUTF8(string(output), "�")
		if len(message) > webEvaluateMaxError {
			message = message[:webEvaluateMaxError]
			for !utf8.ValidString(message) {
				message = message[:len(message)-1]
			}
		}
		return result, webEvaluateFailure(code, message)
	}
	logs, err := webEvaluateRead(ctx, module, "logs_pointer", "logs_length", webEvaluateMaxConsole)
	if err != nil {
		return result, err
	}
	if !utf8.Valid(output) || !utf8.Valid(logs) {
		return result, webEvaluateFailure("evaluation_invalid_output", "Evaluation output is not UTF-8")
	}
	return webEvaluateOutput{JSON: output, Console: string(logs)}, nil
}

func webEvaluateRead(ctx context.Context, module api.Module, pointer, length string, limit int) ([]byte, error) {
	p, err := module.ExportedFunction(pointer).Call(ctx)
	if err != nil || len(p) != 1 || p[0] > uint64(^uint32(0)) {
		return nil, webEvaluateFailure("evaluation_invalid_output", "Invalid evaluator output address")
	}
	n, err := module.ExportedFunction(length).Call(ctx)
	if err != nil || len(n) != 1 || n[0] > uint64(limit) {
		return nil, webEvaluateFailure("evaluation_output_limit", "Evaluation output exceeds its byte budget")
	}
	data, ok := module.Memory().Read(uint32(p[0]), uint32(n[0]))
	if !ok {
		return nil, webEvaluateFailure("evaluation_invalid_output", "Invalid evaluator output range")
	}
	return append([]byte{}, data...), nil
}
